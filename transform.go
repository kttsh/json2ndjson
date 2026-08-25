package main

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"strings"
)

// dedupeMemoryNote は --dedupe-key のメモリ特性に関する注記(要件 9.4 / NFR-05)。
//
// 重複排除のキー集合はレコード数に比例して増加し、NFR-02 のメモリ上限(目安 256 MB)に
// 含まれる。数百万件規模では上限を超えうるため、その旨をヘルプ文言へ載せる必要がある。
// --help の出力そのものはタスク 4.1(cli.go / version.go)の責務であり、本ファイルは
// 文言だけを確定して引き渡す。1 行の文言であることを前提に埋め込んでよい。
const dedupeMemoryNote = "--dedupe-key はキー集合をレコード数に比例してメモリ上に保持します。" +
	"この集合はメモリ使用量の上限(目安 256 MB)に含まれ、数百万件規模のキーでは上限を超えることがあります。"

// errNotFound は JSON Pointer の指定位置がレコード内に存在しないことを表す番兵エラー。
// 呼び出し側は errors.Is で判定する。終了コードへの写像は呼び出し側の責務で、
// --cursor-key の場合は終了コード 5(要件 5.5)、--dedupe-key の場合は
// 「重複排除の対象外」として扱う(Pipeline.Apply のコメント参照)。
var errNotFound = errors.New("指定位置が存在しません")

// newRecordDecoder はレコード 1 件を読み直すための Decoder を作る。
// オプションは convert コンポーネントと同一に揃える(IMPL-07)。
// Q-10(不正 UTF-8 の扱い)の決定時に変更するのは、convert 側と本関数の 2 箇所のみ。
func newRecordDecoder(rec jsontext.Value) *jsontext.Decoder {
	return jsontext.NewDecoder(bytes.NewReader(rec),
		jsontext.AllowInvalidUTF8(true),
		jsontext.AllowDuplicateNames(true))
}

// newRecordEncoder は整形パスの再エンコード用 Encoder を作る。
//
// PreserveRawStrings(true) は本設計の要となる指定で、入力に含まれていた
// \uXXXX などのエスケープ済みシーケンスをそのまま出力へ通す(要件 3.9、Q-09 の緩和策)。
// これがないと Encoder が文字列を正規化し、字句が変化する。
// 既定では Multiline も EscapeForHTML も無効なため、出力はコンパクト形式になる。
func newRecordEncoder(w io.Writer) *jsontext.Encoder {
	return jsontext.NewEncoder(w,
		jsontext.PreserveRawStrings(true),
		jsontext.AllowInvalidUTF8(true),
		jsontext.AllowDuplicateNames(true))
}

// Pipeline はレコード単位の任意整形(キー名置換・固定値追加・サイズ上限・重複排除)を
// 固定順序で適用する(design.md「transform(整形・抽出)」)。
//
// 適用順序は次に固定する。順序が結果に影響するため設計で定めたものであり、変更してはならない。
//  1. キー名置換(FR-33、要件 6.2)— すべての階層のキー名の - を _ にする
//  2. 固定値追加(FR-34、要件 6.3/6.4)— トップレベルにのみ追加する
//  3. サイズ上限判定(NFR-04、要件 6.5/6.6)— 1 と 2 を適用した後のバイト数で判定する
//  4. 重複排除(FR-32、要件 6.1)— キーは 1 の置換「後」の名前で解決する
//
// 状態としては重複排除のキー集合だけを持ち、その寿命は Pipeline 1 本(= 1 回の実行)に
// 閉じる。追記モードの既存ファイル内容は対象外である(要件 6.1)。
type Pipeline struct {
	hyphenToUnder  bool
	addFields      []AddField
	maxRecordBytes int64
	skipOversize   bool
	dedupeKey      *Pointer

	// seen は重複排除のキー集合。dedupeKey が nil のときは nil。
	// 値は抽出した位置の compact 済み生バイト列の string(design.md)。
	seen map[string]struct{}

	// warn は --skip-oversize の警告の出力先。nil なら警告を書かない。
	// transform は os.Stderr を直接参照しない(stderr は main が所有する)。
	warn io.Writer
}

// newPipeline は検証済み Config から整形パイプラインを構築する。
// cfg は読み取るだけで保持しない(AddFields のスライスのみ共有するが書き換えない)。
func newPipeline(cfg *Config) *Pipeline {
	p := &Pipeline{
		hyphenToUnder:  cfg.HyphenToUnder,
		addFields:      cfg.AddFields,
		maxRecordBytes: cfg.MaxRecordBytes,
		skipOversize:   cfg.SkipOversize,
		dedupeKey:      cfg.DedupeKey,
	}
	if p.dedupeKey != nil {
		p.seen = make(map[string]struct{})
	}
	return p
}

// SetWarnWriter は --skip-oversize の警告の出力先を設定する(要件 6.6)。
//
// design.md は convert / transform が stdout・stderr へ書かないことを不変条件としており、
// stderr は main が所有する。そのため transform は os.Stderr を直接参照せず、
// 出力先を外から注入させる。main(タスク 5.1)が自身の stderr を渡す。
// 未設定(nil)の場合、警告は書かれないがスキップ動作そのものは変わらない。
func (p *Pipeline) SetWarnWriter(w io.Writer) {
	p.warn = w
}

// Apply はレコード 1 件に整形を適用する。
//
// 戻り値 skip=true はそのレコードを出力しないことを表す(重複排除による除外、
// または --skip-oversize による除外)。skip=true のとき out は nil、err は nil である。
//
// Preconditions: rec は Decoder を通過した構文的に妥当な JSON 値で、コンパクト済みであること
// (convert が Value.Compact 済みの値を渡す)。
// Postconditions: out はコンパクト形式で、値トークンの字句は入力から 1 バイトも変わらない。
// Invariants: rec を書き換えない。整形オプションが 1 つも有効でない場合は再エンコードを
// 行わず rec をそのまま返すため、字句の同一性は偶然ではなく構造的に保証される。
func (p *Pipeline) Apply(rec jsontext.Value) (jsontext.Value, bool, error) {
	out := rec

	// (1) キー名置換 →(2)固定値追加。どちらも再エンコードを要するため 1 パスで行う。
	if p.needsRewrite() {
		rewritten, err := p.rewrite(rec)
		if err != nil {
			return nil, false, err
		}
		out = rewritten
	}

	// (3) サイズ上限判定。1・2 を適用した後の最終的なバイト数で判定する。
	// 要件 6.5 は「上限バイト数を超えた場合」なので、上限と同一バイト数は違反ではない。
	if p.maxRecordBytes > 0 && int64(len(out)) > p.maxRecordBytes {
		if p.skipOversize {
			p.warnOversize(int64(len(out)))
			return nil, true, nil
		}
		return nil, false, &ExitError{
			Code: ExitLocation,
			Err: fmt.Errorf("レコードが --max-record-bytes の上限を超えました(実サイズ %d バイト、上限 %d バイト)",
				len(out), p.maxRecordBytes),
		}
	}

	// (4) 重複排除。上限超過でスキップしたレコードはここへ到達しないため、
	// スキップされたレコードのキーが集合へ登録されることはない。
	if p.dedupeKey != nil {
		dup, err := p.isDuplicate(out)
		if err != nil {
			return nil, false, err
		}
		if dup {
			return nil, true, nil
		}
	}

	return out, false, nil
}

// needsRewrite は再エンコードが必要かを返す。
// キー名置換も固定値追加も指定されていなければ、レコードは 1 バイトも触らずに素通しできる。
func (p *Pipeline) needsRewrite() bool {
	return p.hyphenToUnder || len(p.addFields) > 0
}

// warnOversize は --skip-oversize によるスキップを警告する(要件 6.6)。
// 出力先が設定されていなければ何もしない。書き込み失敗は無視する
// (警告の失敗で変換全体を落とすと、継続処理という要件 6.6 の意図が損なわれるため)。
func (p *Pipeline) warnOversize(size int64) {
	if p.warn == nil {
		return
	}
	fmt.Fprintf(p.warn, "警告: レコードが --max-record-bytes の上限を超えたためスキップしました(実サイズ %d バイト、上限 %d バイト)\n",
		size, p.maxRecordBytes)
}

// isDuplicate は重複排除キーの値で既出かを判定し、初出なら集合へ登録する(要件 6.1)。
//
// 指定位置が存在しないレコードは「比較すべき値を持たない」ため重複排除の対象外とし、
// 常に出力する。要件 6.1 が定めるのは「指定位置の値が同一のレコード」の扱いだけであり、
// また終了コード 5 は要件 7.4 で --path / --cursor-key の位置不正に割り当てられていて
// --dedupe-key を含まない。したがって欠落をエラーにはしない。
func (p *Pipeline) isDuplicate(rec jsontext.Value) (bool, error) {
	key, err := extract(rec, *p.dedupeKey)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return false, nil
		}
		return false, &ExitError{
			Code: ExitInternal,
			Err:  fmt.Errorf("--dedupe-key %q の値の抽出に失敗しました: %w", pointerText(*p.dedupeKey), err),
		}
	}
	if _, ok := p.seen[string(key)]; ok {
		return true, nil
	}
	// string(key) はキーの探索と登録の双方でコピーを 1 回だけ行う
	// (Go のコンパイラはマップ探索の string(...) 変換を確保なしで扱う)。
	p.seen[string(key)] = struct{}{}
	return false, nil
}

// rewrite はキー名置換と固定値追加をトークン単位の再エンコードで適用する。
//
// 値トークンは jsontext.Value / 生トークンのまま Encoder へ渡すため、数値の字句や
// 文字列のエスケープは変化しない(要件 3.7〜3.9 の維持)。any への展開・Unmarshal は行わない(IMPL-05)。
func (p *Pipeline) rewrite(rec jsontext.Value) (jsontext.Value, error) {
	addToTop := len(p.addFields) > 0
	if addToTop && rec.Kind() != '{' {
		// 要件は非オブジェクトのレコードを規定していないが、--add-field は
		// 「トップレベルにキーを追加する」機能であり適用不能なため引数不正とする
		// (design.md「transform」の設計判断)。
		return nil, &ExitError{
			Code: ExitUsage,
			Err: fmt.Errorf("--add-field はレコードのトップレベルにキーを追加しますが、レコードが%sのため適用できません",
				kindLabel(rec.Kind())),
		}
	}

	dec := newRecordDecoder(rec)
	var buf bytes.Buffer
	buf.Grow(len(rec) + p.addFieldsSizeHint())
	enc := newRecordEncoder(&buf)

	var err error
	if addToTop {
		err = p.copyTopLevelObject(dec, enc)
	} else {
		err = p.copyValue(dec, enc)
	}
	if err != nil {
		var ee *ExitError
		if errors.As(err, &ee) {
			return nil, err
		}
		// レコードは Decoder を通過済みという事前条件が破れている場合にのみ到達する。
		return nil, &ExitError{
			Code: ExitInternal,
			Err:  fmt.Errorf("レコードの再エンコードに失敗しました: %w", err),
		}
	}

	// Encoder はトップレベルの値を 1 つ書き終えるごとに改行を付ける。
	// 1 行 1 レコードの改行は output コンポーネントが cfg.Newline で付けるため、ここでは取り除く。
	return jsontext.Value(bytes.TrimSuffix(buf.Bytes(), []byte("\n"))), nil
}

// addFieldsSizeHint は固定値追加で増えるおおよそのバイト数を返す(バッファ確保のヒント)。
func (p *Pipeline) addFieldsSizeHint() int {
	n := 0
	for _, f := range p.addFields {
		n += len(f.Key) + len(f.Value) + len(`"":"",`)
	}
	return n
}

// copyTopLevelObject はトップレベルのオブジェクトを写しつつ、末尾へ固定値フィールドを追加する。
// 追加するキーはキー名置換「後」の既存キーと比較し、衝突していれば終了コード 1 とする(要件 6.4)。
func (p *Pipeline) copyTopLevelObject(dec *jsontext.Decoder, enc *jsontext.Encoder) error {
	if _, err := dec.ReadToken(); err != nil { // '{'
		return err
	}
	if err := enc.WriteToken(jsontext.BeginObject); err != nil {
		return err
	}

	existing := make(map[string]struct{})
	for dec.PeekKind() != '}' {
		name, err := dec.ReadValue()
		if err != nil {
			return err
		}
		// キー名置換 → 固定値追加、の順序をここで具体化する。
		// 衝突判定は置換後の名前で行うため、"a-b" は --add-field a_b と衝突する。
		if p.hyphenToUnder {
			name = rewriteKeyLexeme(name)
		}
		decoded, err := jsontext.AppendUnquote(nil, name)
		if err != nil {
			return fmt.Errorf("キー名 %s の解釈に失敗しました: %w", name, err)
		}
		existing[string(decoded)] = struct{}{}

		if err := enc.WriteValue(name); err != nil {
			return err
		}
		if err := p.copyValue(dec, enc); err != nil {
			return err
		}
	}
	if _, err := dec.ReadToken(); err != nil { // '}'
		return err
	}

	for _, f := range p.addFields {
		if _, ok := existing[f.Key]; ok {
			return &ExitError{
				Code: ExitUsage,
				Err:  fmt.Errorf("--add-field のキー %q がレコードの既存キーと衝突しています", f.Key),
			}
		}
		if err := enc.WriteToken(jsontext.String(f.Key)); err != nil {
			return err
		}
		if err := enc.WriteToken(jsontext.String(f.Value)); err != nil {
			return err
		}
	}

	return enc.WriteToken(jsontext.EndObject)
}

// copyValue は Decoder の現在位置にある値 1 つを Encoder へ写す。
//
// キー名置換が不要なら、値全体を 1 回の ReadValue / WriteValue で通す(部分木の走査を省く)。
// 必要な場合はオブジェクトと配列を開いて再帰的に降り、すべての階層のキー名を置換する(要件 6.2)。
func (p *Pipeline) copyValue(dec *jsontext.Decoder, enc *jsontext.Encoder) error {
	if !p.hyphenToUnder {
		v, err := dec.ReadValue()
		if err != nil {
			return err
		}
		return enc.WriteValue(v)
	}

	switch dec.PeekKind() {
	case '{':
		if _, err := dec.ReadToken(); err != nil {
			return err
		}
		if err := enc.WriteToken(jsontext.BeginObject); err != nil {
			return err
		}
		for dec.PeekKind() != '}' {
			// 不正な入力で PeekKind が 0 を返す場合も、この ReadValue がエラーを返して
			// ループを抜けるため、無限ループにはならない。
			name, err := dec.ReadValue()
			if err != nil {
				return err
			}
			if err := enc.WriteValue(rewriteKeyLexeme(name)); err != nil {
				return err
			}
			if err := p.copyValue(dec, enc); err != nil {
				return err
			}
		}
		if _, err := dec.ReadToken(); err != nil {
			return err
		}
		return enc.WriteToken(jsontext.EndObject)

	case '[':
		if _, err := dec.ReadToken(); err != nil {
			return err
		}
		if err := enc.WriteToken(jsontext.BeginArray); err != nil {
			return err
		}
		for dec.PeekKind() != ']' {
			if err := p.copyValue(dec, enc); err != nil {
				return err
			}
		}
		if _, err := dec.ReadToken(); err != nil {
			return err
		}
		return enc.WriteToken(jsontext.EndArray)

	default:
		v, err := dec.ReadValue()
		if err != nil {
			return err
		}
		return enc.WriteValue(v)
	}
}

// rewriteKeyLexeme はキー名の生字句(引用符込みの JSON 文字列)に含まれるハイフンを
// アンダースコアへ置換して返す(要件 6.2 / FR-33)。
//
// 生の '-' だけでなく \u002d / \u002D も置換対象とする。どちらも同じ文字 U+002D を
// 表すため、片方だけを置換すると同じキー名が表記によって別扱いになってしまう。
// 置換以外のバイトは 1 バイトも変えないため、他の \uXXXX や非 ASCII の字句は保たれる。
// 置換対象がなければ入力のスライスをそのまま返す(確保なし)。
func rewriteKeyLexeme(name jsontext.Value) jsontext.Value {
	var out []byte // nil のうちは「置換対象がまだ現れていない」ことを表す
	copied := 0    // out へ未コピーの領域の先頭

	for i := 0; i < len(name); {
		switch {
		case name[i] == '\\' && i+1 < len(name):
			// \\ や \" のような 2 バイトのエスケープはまとめて読み飛ばす。
			// これにより "\\u002d"(エスケープされたバックスラッシュ + 文字列 u002d)を
			// ハイフンのエスケープと誤認しない。
			if name[i+1] == 'u' && i+6 <= len(name) && isHyphenHex(name[i+2:i+6]) {
				out = append(appendUncopied(out, name, copied, i), '_')
				i += 6
				copied = i
				continue
			}
			i += 2

		case name[i] == '-':
			out = append(appendUncopied(out, name, copied, i), '_')
			i++
			copied = i

		default:
			i++
		}
	}

	if out == nil {
		return name
	}
	return jsontext.Value(append(out, name[copied:]...))
}

// appendUncopied は out へ name[copied:i] を追記する。out が nil なら先に確保する。
func appendUncopied(out []byte, name jsontext.Value, copied, i int) []byte {
	if out == nil {
		out = make([]byte, 0, len(name))
	}
	return append(out, name[copied:i]...)
}

// isHyphenHex は \u エスケープの 4 桁 16 進数が U+002D(ハイフンマイナス)かを返す。
func isHyphenHex(hex []byte) bool {
	return hex[0] == '0' && hex[1] == '0' && hex[2] == '2' && (hex[3] == 'd' || hex[3] == 'D')
}

// extract は rec 内の ptr 位置にある値を生字句のまま返す(要件 5.3、6.1)。
//
// レコードを新しい Decoder で再走査して探索し、any へ展開しない(IMPL-10)。
// 指定位置が存在しない場合は errNotFound を包んだエラーを返す。終了コードへの写像は
// 呼び出し側の責務であり、--cursor-key では終了コード 5 になる(要件 5.5)。
// 値の種別(スカラーか否か)は判定しない。カーソル値としての妥当性検査は
// extractCursorValue が行う。
func extract(rec jsontext.Value, ptr Pointer) (jsontext.Value, error) {
	dec := newRecordDecoder(rec)

	for i, token := range ptr {
		switch dec.PeekKind() {
		case '{':
			if err := descendObject(dec, token); err != nil {
				return nil, wrapPointerError(ptr, i, err)
			}
		case '[':
			// 対象が配列だと分かった時点でのみ添字の文法(0 | [1-9][0-9]*)を要求する。
			// "-"(配列末尾の次)や先頭ゼロは添字ではないため、位置なしとして扱う。
			idx, ok := parseArrayIndex(token)
			if !ok {
				return nil, wrapPointerError(ptr, i, fmt.Errorf("配列に対する参照トークン %q が添字ではありません: %w", token, errNotFound))
			}
			if err := descendArray(dec, idx); err != nil {
				return nil, wrapPointerError(ptr, i, err)
			}
		default:
			return nil, wrapPointerError(ptr, i, fmt.Errorf("%sの内側へは降りられません: %w", kindLabel(dec.PeekKind()), errNotFound))
		}
	}

	v, err := dec.ReadValue()
	if err != nil {
		return nil, err
	}
	// Decoder の内部バッファや rec の記憶領域を参照し続けないよう写しを返す。
	return v.Clone(), nil
}

// descendObject はオブジェクトを走査し、name に一致するメンバの値の直前で走査を止める。
func descendObject(dec *jsontext.Decoder, name string) error {
	if _, err := dec.ReadToken(); err != nil { // '{'
		return err
	}
	for dec.PeekKind() != '}' {
		key, err := dec.ReadValue()
		if err != nil {
			return err
		}
		decoded, err := jsontext.AppendUnquote(nil, key)
		if err != nil {
			return err
		}
		if string(decoded) == name {
			return nil // Decoder は一致したメンバの値の直前にある
		}
		if err := dec.SkipValue(); err != nil {
			return err
		}
	}
	return fmt.Errorf("キー %q がありません: %w", name, errNotFound)
}

// descendArray は配列を走査し、idx 番目の要素の直前で走査を止める。
func descendArray(dec *jsontext.Decoder, idx int) error {
	if _, err := dec.ReadToken(); err != nil { // '['
		return err
	}
	for range idx {
		if dec.PeekKind() == ']' {
			return fmt.Errorf("配列の要素数が添字 %d に足りません: %w", idx, errNotFound)
		}
		if err := dec.SkipValue(); err != nil {
			return err
		}
	}
	if dec.PeekKind() == ']' {
		return fmt.Errorf("配列の要素数が添字 %d に足りません: %w", idx, errNotFound)
	}
	return nil
}

// extractCursorValue は --cursor-key の位置の値を結果行 last_id 用の文字列へ整形する
// (要件 5.3、5.5、5.6 / FR-29〜FR-31)。
//
// 整形規則(design.md「transform / Implementation Notes」):
//   - 文字列は引用符を外して内容のみを返す(エスケープは解釈する)
//   - 数値・真偽値・null は生字句のまま返す
//
// 結果行は key=value を空白区切りで並べた 1 行であり、値は ASCII の範囲に収める必要がある。
// そのため非スカラー(オブジェクト/配列)、ASCII 範囲外、空白、制御文字を含む値は
// 結果行に載せられないため、位置不正として終了コード 5 で拒否する。
// 位置が存在しない場合(要件 5.5)も終了コード 5 とする。
func extractCursorValue(rec jsontext.Value, ptr Pointer) (string, error) {
	v, err := extract(rec, ptr)
	if err != nil {
		return "", &ExitError{
			Code: ExitLocation,
			Err:  fmt.Errorf("--cursor-key %q の値を取得できません: %w", pointerText(ptr), err),
		}
	}

	var content []byte
	switch v.Kind() {
	case '"':
		content, err = jsontext.AppendUnquote(nil, v)
		if err != nil {
			return "", &ExitError{
				Code: ExitLocation,
				Err:  fmt.Errorf("--cursor-key %q の値を解釈できません: %w", pointerText(ptr), err),
			}
		}
	case '0', 't', 'f', 'n':
		content = v
	default:
		return "", &ExitError{
			Code: ExitLocation,
			Err: fmt.Errorf("--cursor-key %q の値が%sです。結果行に載せられるのはスカラー値のみです",
				pointerText(ptr), kindLabel(v.Kind())),
		}
	}

	// 結果行は空白区切りのため、空白・制御文字・非 ASCII を含む値は使えない(FR-31、FR-42)。
	for i, b := range content {
		if b < 0x21 || b > 0x7e {
			return "", &ExitError{
				Code: ExitLocation,
				Err: fmt.Errorf("--cursor-key %q の値の %d バイト目が結果行に使えない文字(0x%02x)です。値は空白・制御文字を含まない ASCII である必要があります",
					pointerText(ptr), i+1, b),
			}
		}
	}
	return string(content), nil
}

// pointerText は参照トークン列を RFC 6901 の JSON Pointer 文字列へ戻す(エラーメッセージ用)。
// 空スライスはルートを表す空文字列になる。
func pointerText(ptr Pointer) string {
	var b strings.Builder
	for _, token := range ptr {
		b.WriteByte('/')
		// エスケープは "~" が先。順序を逆にすると "/" 由来の "~1" が "~01" になる。
		b.WriteString(strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1"))
	}
	return b.String()
}

// wrapPointerError は探索が失敗した位置(何番目の参照トークンか)を添えてエラーを包む。
func wrapPointerError(ptr Pointer, i int, err error) error {
	return fmt.Errorf("JSON Pointer %q の %d 番目の参照トークンで探索に失敗しました: %w", pointerText(ptr), i+1, err)
}

// kindLabel は JSON 値の種別を日本語のエラーメッセージ用の語にする。
func kindLabel(k jsontext.Kind) string {
	switch k {
	case '{':
		return "オブジェクト"
	case '[':
		return "配列"
	case '"':
		return "文字列"
	case '0':
		return "数値"
	case 't', 'f':
		return "真偽値"
	case 'n':
		return "null"
	default:
		return "不明な種別の値"
	}
}
