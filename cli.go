package main

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
)

// AddField は --add-field で各レコードのトップレベルへ追加する固定値フィールド(FR-34)。
type AddField struct {
	// Key は追加するキー名。
	Key string
	// Value は追加する文字列値。
	Value string
}

// Config は検証済みの実行設定。全コンポーネントが参照する唯一の設定表現であり、
// 契約は design.md「cli(引数解析)/ Service Interface」に従う。
//
// 本タスクでは構造体の骨格(型定義)のみを用意する。
// コマンドライン引数の解析と検証(parseArgs)はタスク 4.1 の責務であり、
// タスク 4.1 はこの構造体定義に手を入れない。
type Config struct {
	// Inputs は --in で指定された入力ファイルのパス。
	// ファイル名(ベース名、同名時はフルパス)の昇順に整列済み(FR-02)。
	Inputs []string
	// Out は --out で指定された出力ファイルの絶対パス(FR-20)。
	Out string
	// Path は --path で指定された変換対象位置。空スライスはルート(FR-05)。
	Path Pointer
	// Append は --append(既存ファイルへの追記モード)(FR-22)。
	Append bool
	// Overwrite は --overwrite(新規モードでの既存ファイル上書き許可)(FR-26)。
	// Append との同時指定は引数不正(FR-37)。
	Overwrite bool
	// Newline は行の改行コード。"\n" または "\r\n"。既定は "\n"(FR-12)。
	Newline string
	// TrailingNewline は最終行にも改行を付けるか。
	// 既定は true で、--no-trailing-newline 指定時に false(FR-14)。
	TrailingNewline bool
	// CursorKey は --cursor-key で指定されたカーソル値の位置。nil は未指定(FR-29)。
	CursorKey *Pointer
	// DedupeKey は --dedupe-key で指定された重複排除キーの位置。nil は未指定(FR-32)。
	DedupeKey *Pointer
	// HyphenToUnder は --key-hyphen-to-underscore(全階層のキー名の - を _ へ置換)(FR-33)。
	HyphenToUnder bool
	// AddFields は --add-field で追加する固定値フィールド。出現順を保持する(FR-34)。
	AddFields []AddField
	// MaxRecordBytes は --max-record-bytes で指定した 1 レコードの上限バイト数。
	// 0 は無制限(NFR-04)。
	MaxRecordBytes int64
	// SkipOversize は --skip-oversize(上限超過レコードを警告してスキップし処理を継続)。
	// MaxRecordBytes なしの単独指定は引数不正(NFR-04、design.md の設計判断)。
	SkipOversize bool
}

// 番兵エラー。--version / --help は「異常」ではなく「表示して終了コード 0」の指示であり
// (要件 7.5 / 7.6)、parseArgs は表示も os.Exit も行わずにこの値を返すだけとする。
// 表示先(stdout)と終了コードの決定は main の責務(IMPL-19/IMPL-20)。
//
// いずれも *ExitError ではない。main が errors.As(*ExitError) で終了コードを取り出す
// 経路に載せてしまうと、正常終了であるはずの表示が引数不正(1)になるため。
var (
	// errShowVersion は --version が指定されたことを表す(要件 7.5)。
	errShowVersion = errors.New("バージョンの表示要求")
	// errShowHelp は --help が指定されたことを表す(要件 7.6)。
	errShowHelp = errors.New("使い方の表示要求")
)

// 改行コードの選択肢(--newline、要件 3.3 / FR-12)。
const (
	newlineLF   = "lf"
	newlineCRLF = "crlf"
)

// usageErrorf は引数不正(終了コード 1、要件 7.3 / FR-37)のエラーを組み立てる。
// メッセージは人間向けの日本語とし(要件 8.2 / FR-41)、必ず問題のある引数名を含める。
func usageErrorf(format string, args ...any) error {
	return &ExitError{Code: ExitUsage, Err: fmt.Errorf(format, args...)}
}

// parseArgs はコマンドライン引数(os.Args[1:] 相当)を検証済みの Config へ変換する
// (design.md「cli(引数解析)」)。
//
// 戻り値は次の 3 通りに限られる。
//   - (*Config, nil): 検証を通過した設定。以降のコンポーネントはこれだけを参照する
//   - (nil, errShowVersion) / (nil, errShowHelp): 表示して終了コード 0(要件 7.5 / 7.6)
//   - (nil, *ExitError{Code: ExitUsage}): 引数不正(要件 7.3)
//
// 設定はすべて引数から決まり、環境変数や設定ファイルは一切参照しない(要件 7.1 / FR-35)。
// 対話的な入力待ちも行わない(要件 7.2 / FR-36)。
//
// 出力の規律: 本関数は stdout にも stderr にも一切書かない。flag パッケージの既定動作
// (使い方を stderr へ出力し os.Exit(2) する)は FlagSet の出力先を io.Discard に、
// エラー方針を ContinueOnError にすることで抑止し、理由は戻り値のエラーとして返す。
// 表示先を選ぶのは main であり、標準出力は結果行専用である(要件 8.1 / FR-40、
// design.md「flag の既定エラー出力は stderr に限定し、stdout を汚さない」)。
//
// 呼び出しごとに FlagSet と蓄積先を新しく確保するため、複数回呼んでも状態を共有しない。
func parseArgs(args []string) (*Config, error) {
	fs := flag.NewFlagSet("json2ndjson", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	// 使い方の文言は usageText が一括して持つ。flag の自動生成は使わない(要件 11.2:
	// ログを持たない本ツールではヘルプが運用者の唯一の文書であり、既定の羅列では足りない)。
	fs.Usage = func() {}

	// --in と --add-field は複数指定を受けるため flag.Func で蓄積する(IMPL-18)。
	// ここでは形式検証を行わず生の値を集めるだけとし、検証は解析後にまとめて行う。
	// flag.Func の中で失敗すると、メッセージが flag 側の英語書式("invalid value ...
	// for flag -in")に包まれ、引数名も "-in" 形式になってしまうため。
	var inputs []string
	fs.Func("in", "入力 JSON ファイル(複数指定可)", func(v string) error {
		inputs = append(inputs, v)
		return nil
	})
	var rawAddFields []string
	fs.Func("add-field", "各レコードへ追加する固定値(キー=値、複数指定可)", func(v string) error {
		rawAddFields = append(rawAddFields, v)
		return nil
	})

	out := fs.String("out", "", "出力 NDJSON ファイル(絶対パス)")
	pathStr := fs.String("path", "", "変換対象位置の JSON Pointer(既定はルート)")
	appendMode := fs.Bool("append", false, "既存の出力ファイルへ追記する")
	overwrite := fs.Bool("overwrite", false, "既存の出力ファイルを上書きする")
	newline := fs.String("newline", newlineLF, "行の改行コード(lf|crlf)")
	noTrailingNewline := fs.Bool("no-trailing-newline", false, "最終行に改行を付けない")
	cursorKey := fs.String("cursor-key", "", "カーソル値の JSON Pointer")
	dedupeKey := fs.String("dedupe-key", "", "重複排除キーの JSON Pointer")
	hyphenToUnder := fs.Bool("key-hyphen-to-underscore", false, "全階層のキー名の - を _ へ置換する")
	maxRecordBytes := fs.Int64("max-record-bytes", 0, "1 レコードの上限バイト数")
	skipOversize := fs.Bool("skip-oversize", false, "上限超過レコードを警告してスキップする")
	showVersion := fs.Bool("version", false, "バージョンを表示して終了する")
	showHelp := fs.Bool("help", false, "使い方を表示して終了する")

	if err := fs.Parse(args); err != nil {
		// flag は未定義の -h を「使い方の要求」として ErrHelp で返す。
		// 要件 7.6 の意図(使い方の表示は正常終了)に合わせて番兵へ写像する。
		if errors.Is(err, flag.ErrHelp) {
			return nil, errShowHelp
		}
		// 未知の引数・値の欠落・値の型不一致。flag 側のメッセージが引数名を含むため、
		// 日本語の文脈を付けてそのまま原因として包む(要件 7.3)。
		return nil, usageErrorf("引数が不正です(未知の引数、値の欠落、または値の形式誤り): %w", err)
	}

	// 表示要求は他の検証より優先する。--version / --help だけを渡した呼び出しが
	// 必須引数の欠落で失敗しては、要件 7.5 / 7.6 を満たせない。
	if *showVersion {
		return nil, errShowVersion
	}
	if *showHelp {
		return nil, errShowHelp
	}

	// flag は最初の非フラグ引数で解析を打ち切る。残りは未知の引数として扱う(要件 7.3)。
	if fs.NArg() > 0 {
		return nil, usageErrorf("解釈できない引数があります: %s", strings.Join(fs.Args(), " "))
	}

	// 必須引数(要件 7.3 / FR-37)。
	if len(inputs) == 0 {
		return nil, usageErrorf("--in は必須です(入力 JSON ファイルを 1 つ以上指定してください)")
	}
	if slices.Contains(inputs, "") {
		return nil, usageErrorf("--in に空のパスが指定されました")
	}
	if *out == "" {
		return nil, usageErrorf("--out は必須です(出力 NDJSON ファイルのパスが空、または未指定です)")
	}

	// 排他的な引数(要件 7.3 / FR-37)。
	if *appendMode && *overwrite {
		return nil, usageErrorf("--append と --overwrite は同時に指定できません")
	}

	// 改行コードの値域(要件 3.3 / FR-12)。
	var newlineSeq string
	switch *newline {
	case newlineLF:
		newlineSeq = "\n"
	case newlineCRLF:
		newlineSeq = "\r\n"
	default:
		return nil, usageErrorf("--newline の値 %q が不正です(%q または %q を指定してください)",
			*newline, newlineLF, newlineCRLF)
	}

	// 明示的に指定された引数の集合。既定値と同じ値が明示指定されうる引数
	// (--max-record-bytes の 0、--cursor-key / --dedupe-key の空文字列)は、
	// 値だけを見ても「指定された」のか「既定のまま」なのかを判別できないため Visit で見る。
	specified := make(map[string]bool, fs.NFlag())
	fs.Visit(func(f *flag.Flag) {
		specified[f.Name] = true
	})

	// JSON Pointer の構文(要件 2.1)。ParsePointer は素の error を返すため、
	// 引数名を添えて終了コード 1 へ写像するのが cli の責務(design.md「Error Handling」)。
	path, err := parsePointerArg("--path", *pathStr)
	if err != nil {
		return nil, err
	}
	cursorPtr, err := optionalPointerArg("--cursor-key", *cursorKey, specified["cursor-key"])
	if err != nil {
		return nil, err
	}
	dedupePtr, err := optionalPointerArg("--dedupe-key", *dedupeKey, specified["dedupe-key"])
	if err != nil {
		return nil, err
	}

	addFields, err := parseAddFields(rawAddFields)
	if err != nil {
		return nil, err
	}

	// レコード上限は正の整数のみ。0 は Config 上「無制限」を意味する内部表現であり、
	// 明示指定は意図の取り違え(無制限のつもりか 0 バイト上限のつもりか)を招くため拒否する。
	// 「指定されたか」は既定値との比較では判定できない(0 が既定値のため)ので specified で見る。
	if specified["max-record-bytes"] && *maxRecordBytes <= 0 {
		return nil, usageErrorf("--max-record-bytes には正の整数を指定してください(指定値: %d)", *maxRecordBytes)
	}
	// --skip-oversize は上限の指定があってはじめて意味を持つ(design.md の設計判断)。
	if *skipOversize && *maxRecordBytes <= 0 {
		return nil, usageErrorf("--skip-oversize は --max-record-bytes と併せて指定してください")
	}

	// 入力はファイル名(ベース名、同名時はフルパス)の昇順に整列する(要件 1.2 / FR-02)。
	// 出力順序の保証はこの 1 箇所だけに依存する(convert は再整列しない)。
	slices.SortFunc(inputs, func(a, b string) int {
		return cmp.Or(
			cmp.Compare(filepath.Base(a), filepath.Base(b)),
			cmp.Compare(a, b),
		)
	})

	return &Config{
		Inputs:          inputs,
		Out:             *out,
		Path:            path,
		Append:          *appendMode,
		Overwrite:       *overwrite,
		Newline:         newlineSeq,
		TrailingNewline: !*noTrailingNewline,
		CursorKey:       cursorPtr,
		DedupeKey:       dedupePtr,
		HyphenToUnder:   *hyphenToUnder,
		AddFields:       addFields,
		MaxRecordBytes:  *maxRecordBytes,
		SkipOversize:    *skipOversize,
	}, nil
}

// parsePointerArg は引数値を JSON Pointer として解析し、失敗を終了コード 1 へ写像する。
// 空文字列はルートを指す有効な値である(要件 2.1)。
func parsePointerArg(name, value string) (Pointer, error) {
	p, err := ParsePointer(value)
	if err != nil {
		return nil, usageErrorf("%s の JSON Pointer が不正です: %w", name, err)
	}
	return p, nil
}

// optionalPointerArg は「引数ごとの省略」を nil として扱うポインタ引数を解析する。
// specified は引数が明示的に指定されたか(fs.Visit で得た事実)。
//
// 空文字列は RFC 6901 ではルートを指す有効な値だが(--path の "" はそのまま受け付ける、
// 要件 2.1)、--cursor-key / --dedupe-key ではルートを指しても意味を持たない。
// かといって空文字列を「未指定」と読み替えると、呼び出し元スクリプトが未設定の変数を
// 展開して --cursor-key "" を渡した場合に黙ってカーソルなしとして動き、結果行では
// 要件 5.4 の「0 件だから last_id なし」と区別できない。--in / --out の空文字列や
// --max-record-bytes の 0 と同じ規律で、明示的な空文字列は引数不正とする(要件 7.3)。
func optionalPointerArg(name, value string, specified bool) (*Pointer, error) {
	if !specified {
		return nil, nil
	}
	if value == "" {
		return nil, usageErrorf("%s に空の JSON Pointer が指定されました(未指定にする場合は引数ごと省略してください)", name)
	}
	p, err := parsePointerArg(name, value)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// parseAddFields は --add-field の生の値を検証して AddField 列へ変換する(要件 6.3 / FR-34)。
//
// 形式は「キー=値」。最初の "=" で分割するため、値に "=" を含められる("a=b=c" は
// キー "a"・値 "b=c")。値は空でもよいが("k=" は空文字列を追加する)、キーは空にできない。
// 同じキーの重複指定は、どちらが勝つかを利用者が予測できないため引数不正とする
// (レコード側の既存キーとの衝突検出は transform の責務、タスク 2.2)。
func parseAddFields(raw []string) ([]AddField, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	fields := make([]AddField, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, v := range raw {
		key, value, found := strings.Cut(v, "=")
		if !found {
			return nil, usageErrorf("--add-field の値 %q は キー=値 の形式ではありません", v)
		}
		if key == "" {
			return nil, usageErrorf("--add-field の値 %q はキーが空です", v)
		}
		if _, dup := seen[key]; dup {
			return nil, usageErrorf("--add-field のキー %q が重複しています", key)
		}
		seen[key] = struct{}{}
		fields = append(fields, AddField{Key: key, Value: value})
	}
	return fields, nil
}
