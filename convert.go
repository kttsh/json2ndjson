package main

import (
	"bufio"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
)

// maxNestingDepth はレコード内部および --path 移動中に許容するネスト深さの上限
// (IMPL-11、要件 10.2 / NFR-08)。深さがこの値「以下」なら通し、超えたものを拒否する
// (タスク 2.4 の境界: 511 可・512 可・513 不可)。
//
// jsontext 自身のネスト上限は 10000 で固定・変更不可のため、本ツールの上限は
// 自前で判定する。移動中は Decoder.StackDepth() を、レコード内部は検証済み生バイトの
// 線形走査(exceedsNestingDepth)を用いる。
const maxNestingDepth = 512

// readBufferSize は入力ファイルの読み取りバッファの大きさ。
// 入力サイズに依存しない一定メモリで処理するため(NFR-01/02)、固定長とする。
const readBufferSize = 64 << 10

// ConvertResult は変換 1 回分の結果(design.md「convert / Service Interface」)。
type ConvertResult struct {
	// Records は出力したレコード数(FR-28)。
	// 重複排除とサイズ超過スキップで落ちたレコードは含まない。
	Records int64
	// Files は処理した入力ファイル数(FR-28)。
	Files int
	// LastValue は最後に出力したレコードの写し。
	// --cursor-key 指定時のみ保持し、指定がなければ nil のままとする。
	// 「写し」であることが本質で、レコードバッファの別名を保持してはならない
	// (整形なしの素通し経路では Pipeline.Apply が入力スライスをそのまま返すため)。
	LastValue jsontext.Value
}

// RecordSink は output が実装する書き込み境界(design.md「convert / Service Interface」)。
// convert は出力ファイルの状態(新規/追記/復旧)を知らない。
type RecordSink interface {
	// WriteRecord は 1 行(compact + 改行)を書き出す。
	//
	// compact のバイト列は呼び出しの間だけ有効である。convert はレコードのバッファを
	// 再利用するため、呼び出しを越えて保持したい実装は自分で写しを取らなければならない
	// (output は受け取った時点で bufio.Writer へ書き出すのでこの制約で足りる)。
	WriteRecord(compact jsontext.Value) error
}

// runConversion は cfg.Inputs を与えられた順に処理し、--path 位置のレコードを
// 1 件ずつ sink へ書き出す(要件 1.1〜1.6、2.2〜2.5、3.1〜3.10、9.1)。
//
// Preconditions: cfg は検証済み。sink は Open 済み。
// Postconditions: 正常時、全レコードが sink へ書き込まれ Records / Files が確定する。
// --cursor-key 指定時は LastValue に最後に出力したレコードの写しが入る。
// Invariants: 入力ファイルを変更しない。stdout / stderr へ書かない。
// 処理順は cfg.Inputs の順(整列は cli の責務であり、ここでは並べ替えない)。
//
// # warn 引数が design.md と異なる理由
//
// design.md が公開するシグネチャは引数 2 つ(cfg, sink)だが、ここでは警告の出力先
// warn を第 3 引数として必ず受け取る。--skip-oversize でスキップしたレコードの警告
// (要件 6.6)の宛先は stderr を所有する main のものであり、convert は stdout / stderr
// へ書かない不変条件があるため os.Stderr を自前で参照できない。したがって引数 2 つの
// 形では、呼び出し元が警告先を渡す手段がなく要件 6.6 を無言で満たせなくなる
// (スキップ動作だけが効き、警告は捨てられる。コンパイル・静的解析・テストのいずれも
// これを検出しない)。入口を 1 本に保ったまま宛先を型で強制するため、
// design.md の 2 引数形ではなくこの形を採る。
//
// warn が nil の場合は「警告を書かない」指定として扱う(Pipeline.SetWarnWriter の
// 既定と同じ無操作)。スキップ動作そのものは warn の有無に影響されない。
// convert は warn を受け流すだけで、自身は決して stdout / stderr を参照しない。
func runConversion(cfg *Config, sink RecordSink, warn io.Writer) (*ConvertResult, error) {
	pipeline := newPipeline(cfg)
	pipeline.SetWarnWriter(warn)

	c := &converter{cfg: cfg, sink: sink, pipeline: pipeline, result: &ConvertResult{}}
	for _, name := range cfg.Inputs {
		if err := c.convertFile(name); err != nil {
			return nil, err
		}
		c.result.Files++
	}
	return c.result, nil
}

// converter は 1 回の変換で共有する状態を持つ。
type converter struct {
	cfg      *Config
	sink     RecordSink
	pipeline *Pipeline
	result   *ConvertResult

	// recBuf はレコード 1 件分の再利用バッファ。
	//
	// Decoder.ReadValue が返すスライスは内部バッファを指しており、次の読み取りで
	// 無効になるうえ、Decoder の使用中は書き換えてはならない。一方 Value.Compact は
	// 対象を書き換える。そのため必ずここへ写してから整形する。
	// 使い回すことで、メモリは最大レコード 1 件分に漸近する(NFR-01/02)。
	recBuf []byte
}

// fileContext は入力ファイル 1 つ分の読み取り状態とエラー報告に必要な情報。
type fileContext struct {
	// name は入力ファイル名(エラーメッセージ用、FR-38)。
	name string
	// bomOffset は読み飛ばした BOM のバイト数。
	// Decoder のオフセットは BOM を含まないため、ファイル先頭からの
	// 絶対バイトオフセットを報告するために加算する。
	bomOffset int64
	dec       *jsontext.Decoder
	// path は --path。エラーメッセージの JSON Pointer の既定値に使う。
	path Pointer
}

// convertFile は入力ファイル 1 つを処理する。
func (c *converter) convertFile(name string) error {
	// 入力は読み取り専用で開き、変更も削除もしない(要件 1.4 / FR-04)。
	f, err := os.Open(name)
	if err != nil {
		return &ExitError{
			Code: ExitInput,
			Err:  fmt.Errorf("入力ファイル %s を開けません: %w", name, err),
		}
	}
	defer f.Close()

	r := bufio.NewReaderSize(f, readBufferSize)
	bom, err := skipUTF8BOM(r)
	if err != nil {
		return &ExitError{
			Code: ExitInput,
			Err:  fmt.Errorf("入力ファイル %s を読み取れません: %w", name, err),
		}
	}

	fc := &fileContext{
		name:      name,
		bomOffset: bom,
		path:      c.cfg.Path,
		// Decoder のオプションは transform 側(newRecordDecoder)と揃える(IMPL-07)。
		// Q-10(不正 UTF-8 の扱い)の決定時に変更するのはこの 2 箇所のみ。
		dec: jsontext.NewDecoder(r,
			jsontext.AllowInvalidUTF8(true),
			jsontext.AllowDuplicateNames(true)),
	}

	if err := c.navigate(fc); err != nil {
		return err
	}
	if err := c.emit(fc); err != nil {
		return err
	}
	// 対象より後ろに構文エラーや余分なトークンがあれば、その入力は JSON として
	// 不正であり全体を失敗とする(要件 1.6)。SkipValue で読み飛ばすだけなので
	// メモリはレコード 1 件分を超えない。
	if err := drainRemainder(fc.dec); err != nil {
		return fc.syntaxError(err)
	}
	return nil
}

// skipUTF8BOM は先頭の UTF-8 BOM(EF BB BF)を読み飛ばし、読み飛ばしたバイト数を返す
// (要件 1.1 / FR-01)。BOM の判定はファイル先頭でのみ行う。
func skipUTF8BOM(r *bufio.Reader) (int64, error) {
	head, err := r.Peek(3)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, err
	}
	if len(head) == 3 && head[0] == 0xEF && head[1] == 0xBB && head[2] == 0xBF {
		if _, err := r.Discard(3); err != nil {
			return 0, err
		}
		return 3, nil
	}
	// BOM がない(または 3 バイト未満の)ファイルはそのまま Decoder へ渡す。
	// 空ファイルは JSON として不正であり、Decoder が構文エラーとして報告する。
	return 0, nil
}

// navigate は --path の位置まで Decoder を進める(要件 2.1)。
//
// 移動中は Decoder.StackDepth() でネスト深さを監視し、上限を超えたら構文エラーとする。
// 位置が存在しない・スカラーの内側へ降りようとした場合は位置不正(終了コード 5)。
func (c *converter) navigate(fc *fileContext) error {
	for i, token := range c.cfg.Path {
		kind, err := peekKind(fc.dec)
		if err != nil {
			return fc.syntaxError(err)
		}

		var found bool
		switch kind {
		case '{':
			found, err = navigateObject(fc.dec, token)
		case '[':
			// 参照トークンが添字であることを要求できるのは、対象が配列だと
			// 判明したこの時点だけ(pointer.go の parseArrayIndex の注記)。
			idx, ok := parseArrayIndex(token)
			if !ok {
				return fc.locationError(i, fmt.Errorf("配列に対する参照トークン %q が添字(0 または [1-9][0-9]*)ではありません", token))
			}
			found, err = navigateArray(fc.dec, idx)
		default:
			// PeekKind は先頭 1 バイトしか見ないため、"nope" のような不正な字句も
			// null として報告される。位置不正(5)と構文エラー(3)を取り違えないよう、
			// 値として妥当かどうかを読み飛ばしで確かめてから分類する。
			if err := fc.dec.SkipValue(); err != nil {
				return fc.syntaxError(err)
			}
			return fc.locationError(i, fmt.Errorf("%sの内側へは降りられません", kindLabel(kind)))
		}
		if err != nil {
			return fc.syntaxError(err)
		}
		if !found {
			return fc.locationError(i, fmt.Errorf("参照トークン %q に対応する位置がありません", token))
		}
		if fc.dec.StackDepth() > maxNestingDepth {
			return fc.depthError(pointerText(c.cfg.Path[:i+1]))
		}
	}
	return nil
}

// emit は --path 位置の値の種別を判定し、レコードを取り出して書き出す。
//
//   - 配列: 各要素を 1 レコードとする(要件 2.2 / FR-06)
//   - オブジェクト: 全体を 1 レコードとする(要件 2.3 / FR-07)
//   - それ以外: 位置不正で終了コード 5(要件 2.4 / FR-08)
func (c *converter) emit(fc *fileContext) error {
	kind, err := peekKind(fc.dec)
	if err != nil {
		return fc.syntaxError(err)
	}
	switch kind {
	case '[':
		return c.emitArray(fc)
	case '{':
		return c.emitSingle(fc)
	default:
		// navigate の default 節と同じ理由で、スカラーの字句が妥当かを先に確かめる。
		if err := fc.dec.SkipValue(); err != nil {
			return fc.syntaxError(err)
		}
		return &ExitError{
			Code: ExitLocation,
			Err: fmt.Errorf("%s: --path %q が指す値は%sです。配列かオブジェクトである必要があります",
				fc.name, pointerText(c.cfg.Path), kindLabel(kind)),
		}
	}
}

// emitArray は配列の要素を 1 件ずつ読み・書き・破棄する(要件 9.1 / NFR-01)。
func (c *converter) emitArray(fc *fileContext) error {
	if _, err := fc.dec.ReadToken(); err != nil { // '['
		return fc.syntaxError(err)
	}
	for index := 0; ; index++ {
		kind, err := peekKind(fc.dec)
		if err != nil {
			return fc.syntaxError(err)
		}
		if kind == ']' {
			break
		}
		raw, err := fc.dec.ReadValue()
		if err != nil {
			return fc.syntaxError(err)
		}
		if err := c.handleRecord(fc, raw, index); err != nil {
			return err
		}
	}
	if _, err := fc.dec.ReadToken(); err != nil { // ']'
		return fc.syntaxError(err)
	}
	return nil
}

// emitSingle はオブジェクト全体を 1 レコードとして読み・書き出す(要件 2.3)。
func (c *converter) emitSingle(fc *fileContext) error {
	raw, err := fc.dec.ReadValue()
	if err != nil {
		return fc.syntaxError(err)
	}
	return c.handleRecord(fc, raw, -1)
}

// handleRecord はレコード 1 件をコンパクト化・深さ検査・整形して書き出す。
// index は配列要素の添字(単一オブジェクトの場合は -1)で、エラー報告の
// JSON Pointer に使う。
func (c *converter) handleRecord(fc *fileContext, raw jsontext.Value, index int) error {
	// Decoder の内部バッファを書き換えないよう、自前のバッファへ写してから Compact する。
	// Compact は空白の除去だけを行い、数値や文字列の字句を変えない(要件 3.1、3.7〜3.9)。
	c.recBuf = append(c.recBuf[:0], raw...)
	rec := jsontext.Value(c.recBuf)
	if err := rec.Compact(); err != nil {
		// ReadValue を通過した値のため通常は到達しない。
		return fc.syntaxError(err)
	}
	c.recBuf = rec

	// 検証済み生バイトの線形走査でネスト深さの上限を判定する(要件 10.2 / IMPL-11)。
	if exceedsNestingDepth(rec, maxNestingDepth) {
		return fc.depthError(recordPointer(fc.path, index))
	}

	out, skip, err := c.pipeline.Apply(rec)
	if err != nil {
		return err
	}
	if skip {
		return nil
	}
	if err := c.sink.WriteRecord(out); err != nil {
		return err
	}

	c.result.Records++
	if c.cfg.CursorKey != nil {
		// Apply は素通し経路で入力スライス(= recBuf)をそのまま返すため、
		// 写しを取らないと次のレコードの読み取りで内容が壊れる。
		// design.md も LastValue を「最終レコードの写し」と定めている。
		c.result.LastValue = out.Clone()
	}
	return nil
}

// exceedsNestingDepth は検証済みの JSON 値 v のネスト深さが limit を超えるかを返す。
//
// 文字列の外側にある '{' と '[' を数え、'}' と ']' で戻す線形走査で判定する。
// 再帰下降ではないため、どれほど深い入力でもスタックを消費しない(NFR-08)。
// 文字列の内側の括弧は数えず、エスケープ("\\" や "\"")を正しく読み飛ばす。
// v は Decoder を通過した構文的に妥当な JSON 値であることを前提とする。
func exceedsNestingDepth(v jsontext.Value, limit int) bool {
	depth := 0
	for i := 0; i < len(v); i++ {
		switch v[i] {
		case '"':
			// 文字列を読み飛ばす。'\' の次の 1 バイトは常に文字列の一部。
			for i++; i < len(v) && v[i] != '"'; i++ {
				if v[i] == '\\' {
					i++
				}
			}
		case '{', '[':
			depth++
			if depth > limit {
				return true
			}
		case '}', ']':
			depth--
		}
	}
	return false
}

// peekKind は次のトークンの種別を返す。
//
// Decoder.PeekKind は誤りが起きると 0 を返してエラーを内部に保持するだけなので、
// 読み取り呼び出しでそのエラーを取り出して返す。これにより「構文エラー・入出力
// エラー・EOF」(終了コード 2 / 3)と「位置不正」(終了コード 5)を取り違えない。
func peekKind(dec *jsontext.Decoder) (jsontext.Kind, error) {
	if kind := dec.PeekKind(); kind != 0 {
		return kind, nil
	}
	if _, err := dec.ReadValue(); err != nil {
		return 0, err
	}
	// PeekKind が 0 を返したのに読み取りが成功することはない。
	return 0, errors.New("JSON の読み取り状態を判定できません")
}

// navigateObject はオブジェクトを走査し、name に一致するメンバの値の直前で止める。
// 見つかったかどうかを返し、見つからない場合のエラー分類は呼び出し側に委ねる。
func navigateObject(dec *jsontext.Decoder, name string) (bool, error) {
	if _, err := dec.ReadToken(); err != nil { // '{'
		return false, err
	}
	for {
		kind, err := peekKind(dec)
		if err != nil {
			return false, err
		}
		if kind == '}' {
			return false, nil
		}
		key, err := dec.ReadValue()
		if err != nil {
			return false, err
		}
		decoded, err := jsontext.AppendUnquote(nil, key)
		if err != nil {
			return false, err
		}
		if string(decoded) == name {
			return true, nil // Decoder は一致したメンバの値の直前にある
		}
		if err := dec.SkipValue(); err != nil {
			return false, err
		}
	}
}

// navigateArray は配列を走査し、idx 番目の要素の直前で止める。
// 要素数が足りなければ false を返す。
func navigateArray(dec *jsontext.Decoder, idx int) (bool, error) {
	if _, err := dec.ReadToken(); err != nil { // '['
		return false, err
	}
	for range idx {
		kind, err := peekKind(dec)
		if err != nil {
			return false, err
		}
		if kind == ']' {
			return false, nil
		}
		if err := dec.SkipValue(); err != nil {
			return false, err
		}
	}
	kind, err := peekKind(dec)
	if err != nil {
		return false, err
	}
	return kind != ']', nil
}

// drainRemainder は残りの文書を末尾まで読み飛ばし、構文エラーがないことを確かめる。
// 値は SkipValue で捨てるため、メモリは増えない。
func drainRemainder(dec *jsontext.Decoder) error {
	for {
		kind := dec.PeekKind()
		if kind == 0 {
			// 文書の終端(io.EOF)か、構文エラー・入出力エラーのいずれか。
			_, err := dec.ReadToken()
			switch {
			case errors.Is(err, io.EOF):
				return nil
			case err != nil:
				return err
			default:
				continue // 読み取りが進んだので次へ(無限ループにはならない)
			}
		}
		var err error
		if kind == '}' || kind == ']' {
			_, err = dec.ReadToken()
		} else {
			// オブジェクトのキーも 1 つの値として読み飛ばせる。
			err = dec.SkipValue()
		}
		if err != nil {
			return err
		}
	}
}

// isInputIOError は err が入力ファイルの入出力エラーかを返す。
//
// jsontext は入出力エラーを構文エラーと同じ error 値として返し、その型は非公開である。
// 一方で終了コードは「入力ファイル不可 = 2」と「JSON 構文エラー = 3」を区別しなければ
// ならない(要件 1.5 / 1.6)。os.File の読み取り失敗は必ず *fs.PathError として
// 包まれてくるため、それを目印にする。
func isInputIOError(err error) bool {
	var pathErr *fs.PathError
	return errors.As(err, &pathErr)
}

// syntaxError は Decoder 由来のエラーを終了コード付きのエラーへ写像する。
//
// 構文エラーにはファイル名・バイトオフセット・JSON Pointer を必ず添える
// (要件 1.6 / FR-38、design.md「Error Categories and Responses」)。
// 位置は jsontext.SyntacticError が持つものを優先し、無い場合は Decoder の
// 現在位置と --path で代用する。BOM を読み飛ばした分はファイル先頭からの
// 絶対オフセットになるよう加算する。
func (fc *fileContext) syntaxError(err error) error {
	if isInputIOError(err) {
		return &ExitError{
			Code: ExitInput,
			Err:  fmt.Errorf("入力ファイル %s を読み取れません: %w", fc.name, err),
		}
	}

	offset := fc.bomOffset + fc.dec.InputOffset()
	pointer := pointerText(fc.path)
	var serr *jsontext.SyntacticError
	if errors.As(err, &serr) {
		offset = fc.bomOffset + serr.ByteOffset
		pointer = string(serr.JSONPointer)
	}
	return &ExitError{
		Code: ExitSyntax,
		Err: fmt.Errorf("%s: JSON の構文エラーです(バイトオフセット %d、JSON Pointer %q): %w",
			fc.name, offset, pointer, err),
	}
}

// depthError はネスト深さの上限超過を構文エラー扱いで返す(要件 10.2、IMPL-11)。
func (fc *fileContext) depthError(pointer string) error {
	return &ExitError{
		Code: ExitSyntax,
		Err: fmt.Errorf("%s: JSON のネストが深すぎます(上限 %d、バイトオフセット %d、JSON Pointer %q)",
			fc.name, maxNestingDepth, fc.bomOffset+fc.dec.InputOffset(), pointer),
	}
}

// locationError は --path の位置不正を終了コード 5 で返す(要件 2.4 / FR-08)。
// i は解決に失敗した参照トークンの位置(0 起点)。
func (fc *fileContext) locationError(i int, cause error) error {
	return &ExitError{
		Code: ExitLocation,
		Err: fmt.Errorf("%s: --path %q の %d 番目の参照トークンで位置を解決できません: %w",
			fc.name, pointerText(fc.path), i+1, cause),
	}
}

// recordPointer はレコードを指す JSON Pointer 文字列を組み立てる。
// index が負の場合(単一オブジェクト)は --path そのものを指す。
func recordPointer(path Pointer, index int) string {
	base := pointerText(path)
	if index < 0 {
		return base
	}
	return base + "/" + strconv.Itoa(index)
}
