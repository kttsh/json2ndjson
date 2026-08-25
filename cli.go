package main

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
