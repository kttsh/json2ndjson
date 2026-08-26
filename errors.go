package main

import "fmt"

// 終了コード体系(要件 7.4、design.md「Error Handling / Error Categories and Responses」)。
// 呼び出し元(DataSpider スクリプト)はこの値だけで失敗理由を分岐する。
// 値は外部契約であり、変更は呼び出し元の再検証を要する(design.md「Revalidation Triggers」)。
const (
	// ExitOK は正常終了。
	ExitOK = 0
	// ExitUsage は引数不正(未知/欠落/排他指定/形式/ポインタ構文/add-field 衝突・非オブジェクト)。
	ExitUsage = 1
	// ExitInput は入力ファイル不可(不存在/権限)。
	ExitInput = 2
	// ExitSyntax は JSON 構文エラー(ネスト深さ超過を含む)。
	ExitSyntax = 3
	// ExitOutput は出力書き込みエラー(権限/ディスク/排他/既存ファイル/ジャーナル破損)。
	ExitOutput = 4
	// ExitLocation は位置不正(--path / --cursor-key の位置、レコードのサイズ超過)。
	ExitLocation = 5
	// ExitInternal は予期しない内部エラー(panic からの回復を含む)。
	ExitInternal = 9
)

// ExitError は終了コードを保持する error。
// 各コンポーネントはエラーを ExitError へ正規化して返すだけとし、
// stderr への出力・出力ファイルの復元・os.Exit は main が 1 箇所で行う(IMPL-19)。
// main は errors.As で Code を取り出す。
type ExitError struct {
	// Code は終了コード。上記 Exit* 定数のいずれか。
	Code int
	// Err は原因エラー。nil を許容する。
	Err error
}

// *ExitError が error インターフェースを満たすことをコンパイル時に保証する。
var _ error = (*ExitError)(nil)

// Error は原因エラーのメッセージをそのまま返す。
// stderr には人間向けメッセージのみを書く規律(要件 8.2、FR-41)のため、
// 原因エラーがあるかぎり終了コードの数値をメッセージへ混ぜない。
// 例外は原因エラーが nil の縮退ケースで、error 契約上メッセージを空文字列に
// できないため、終了コードを含む既定の文言を返す。
func (e *ExitError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("終了コード %d の異常終了", e.Code)
	}
	return e.Err.Error()
}

// Unwrap は原因エラーを返し、errors.Is / errors.As による連鎖のたどりを可能にする。
func (e *ExitError) Unwrap() error {
	return e.Err
}
