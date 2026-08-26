package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// messagePrefix は標準エラー出力へ書くメッセージの先頭に付ける識別子。
// 本ツールは自身のログを持たず、記録は呼び出し元が取得する stdout / stderr /
// 終了コードに集約される(要件 11.2 / NFR-10)。呼び出し元のログでは複数の
// プロセスの出力が混ざりうるため、発信元を明示する。
const messagePrefix = "json2ndjson: "

// main は os.Exit を呼び出す唯一の箇所(design.md「main(エントリ・終了コード集約)」、
// IMPL-19/IMPL-20)。実処理はテスト可能な run に委譲する。
func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run はテスト可能な実行本体で、終了コードを戻り値として返す(要件 7.4)。
//
// フローは design.md「main(エントリ・終了コード集約)」のとおり
// 「引数解析 → 出力オープン → 変換 → カーソル抽出 → 確定 → 結果行出力」を直列に結線する。
//
// # 出力の規律(要件 8.1〜8.3)
//
// 標準出力へ書くのは次の 2 つだけで、いずれも正常終了(終了コード 0)の経路に限られる。
//   - 結果行 1 行(要件 5.1 / FR-27、FR-40)。出力ファイルの確定に成功した後にのみ書く
//   - --version / --help の表示(要件 7.5 / 7.6)
//
// 異常終了の経路では標準出力へ 1 バイトも書かない。標準エラー出力へは異常時の人間向け
// メッセージ(日本語、UTF-8)と、--skip-oversize の警告(要件 6.6)だけを書く。
//
// # panic の扱い(要件 7.4)
//
// 予期しない panic は終了コード 9 へ変換する。回復は run の defer で行うため、
// executeConversion が defer で仕掛けた出力ファイルの中断処理(要件 4.9)は
// 回復より先に走る。したがって panic した場合も出力ファイルは実行前の状態へ戻る。
func run(args []string, stdout, stderr io.Writer) (code int) {
	defer func() {
		if r := recover(); r != nil {
			// 内部エラーの「概要」を報告する(design.md の Error Handling 表)。
			// スタックトレースは出さない: 呼び出し元のログを汚さないためであり、
			// 本ツールの観測点は終了コードと 1 行のメッセージに限る(要件 11.2)。
			fmt.Fprintf(stderr, "%s予期しない内部エラー(panic): %v\n", messagePrefix, r)
			code = ExitInternal
		}
	}()

	cfg, err := parseArgs(args)
	if err != nil {
		// --version / --help は「異常」ではなく表示要求である。番兵エラーは *ExitError
		// ではないため、終了コードへの写像より先に判定しなければ引数不正(1)になる。
		//
		// 表示の書き込み失敗は無視する。結果行と違ってこれらは呼び出し元のループ制御の
		// 入力ではなく、要件 7.5 / 7.6 が定めるのは「表示して終了コード 0」だからである
		// (結果行だけは届かなければ成功を名乗れないため、下で失敗を検査する)。
		switch {
		case errors.Is(err, errShowVersion):
			fmt.Fprintln(stdout, versionLine())
			return ExitOK
		case errors.Is(err, errShowHelp):
			fmt.Fprint(stdout, usageText())
			return ExitOK
		}
		return reportError(stderr, err)
	}

	line, err := executeConversion(cfg, stderr)
	if err != nil {
		return reportError(stderr, err)
	}

	// ここへ到達するのは出力ファイルの確定に成功した後だけ(要件 5.1 / FR-27)。
	if _, err := io.WriteString(stdout, line); err != nil {
		// 出力ファイルは既に確定しているため復元はしない。結果行を届けられない以上、
		// 呼び出し元はループを継続できないので、正常終了として 0 を返してはならない。
		fmt.Fprintf(stderr, "%s結果行を標準出力へ書き出せません: %v\n", messagePrefix, err)
		return ExitInternal
	}
	return ExitOK
}

// executeConversion は出力ファイルのライフサイクルの内側で変換を実行し、
// 確定に成功したときだけ標準出力へ書くべき結果行を返す。
//
// 戻り値の結果行を run が書き出すのは、書き出しを確定(Finalize)より後に置くためである。
// 確定前に書くと、確定に失敗した実行が「成功した」ことを示す結果行を呼び出し元へ
// 渡してしまう(要件 5.1 / FR-27、FR-40)。
//
// エラー時は defer した Abort が出力ファイルを実行前の状態へ戻す(要件 4.9 / FR-39)。
// Finalize に成功した後の Abort は OutputManager 側の状態ガードにより何もしない。
func executeConversion(cfg *Config, stderr io.Writer) (string, error) {
	out, err := openOutput(cfg)
	if err != nil {
		return "", err
	}
	// 異常終了(エラー・panic)のいずれの経路でも出力ファイルを復元する(要件 4.9)。
	defer out.Abort()

	// 第 3 引数は --skip-oversize の警告の宛先(要件 6.6)。stderr を所有するのは
	// main だけなので、ここで渡さないと警告が無言で捨てられる。
	result, err := runConversion(cfg, out, stderr)
	if err != nil {
		return "", err
	}

	cursor, err := cursorValue(cfg, result)
	if err != nil {
		return "", err
	}

	// 結果行の組み立て(= ASCII 検証、要件 5.6)も確定より前に行う。確定の後に
	// 失敗させると、出力ファイルだけが確定して結果行を返せない状態になる。
	line, err := formatResultLine(result, cursor)
	if err != nil {
		return "", err
	}

	if err := out.Finalize(); err != nil {
		return "", err
	}
	return line, nil
}

// cursorValue は結果行に載せる last_id の値を決める(要件 5.3 / 5.4、FR-29)。
//
// 戻り値 nil は「last_id を出力しない」を意味する。--cursor-key が指定されていない場合と、
// 出力したレコードが 0 件の場合がこれにあたる。空文字列の値(JSON の "")と
// 「値なし」を取り違えないよう、有無は *string の nil で表す。
func cursorValue(cfg *Config, result *ConvertResult) (*string, error) {
	if cfg.CursorKey == nil || result.Records == 0 {
		return nil, nil
	}
	if result.LastValue == nil {
		// convert は --cursor-key 指定かつ 1 件以上出力したなら必ず写しを残す。
		// ここへ到達するのは convert 側の不変条件が壊れたときだけである。
		return nil, &ExitError{
			Code: ExitInternal,
			Err:  fmt.Errorf("%d 件を出力したのに最終レコードが保持されていません", result.Records),
		}
	}
	// 値の非引用化と ASCII / 空白 / 制御文字の検証は transform が一括して行う
	// (位置不正・非スカラー・非 ASCII はいずれも終了コード 5)。
	value, err := extractCursorValue(result.LastValue, *cfg.CursorKey)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

// formatResultLine は結果行を組み立てる(要件 5.1、5.2、5.6 / FR-27〜FR-31)。
//
// 形式は `records=<n> files=<n>[ last_id=<v>]` で、単一空白区切りの 1 行、LF 終端
// (design.md「Data Models / 結果行」)。終端は常に LF であり、--newline は
// NDJSON ファイル側の改行コードなので結果行には影響しない。
//
// cursor が nil のときは last_id を出力しない(要件 5.4)。
//
// # design.md との差異
//
// design.md の署名は `formatResultLine(r *ConvertResult, cursor jsontext.Value)` だが、
// ここでは第 2 引数を `*string` とする。カーソル値の非引用化と検証は transform の
// extractCursorValue が既に済ませており(タスク 2.2 が確定した契約)、生の
// jsontext.Value を受け取るとその整形規則を main 側に再実装することになるためである。
// nil を「値なし」に使うことで、JSON の空文字列("")を値として持つ場合と
// --cursor-key 未指定・0 件の場合を取り違えない。
//
// ASCII 検証は last_id の値だけでなく組み立てた行全体に対して行う。カーソル値の検証は
// extractCursorValue が既に済ませているため重複だが、要件 5.6 / FR-31 が保証するのは
// 「標準出力に出る内容」そのものであり、その保証は標準出力へ渡す文字列の上で
// 成立させるのが正しい。records / files の書式が将来変わっても保証が破れない。
func formatResultLine(r *ConvertResult, cursor *string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "records=%d files=%d", r.Records, r.Files)
	if cursor != nil {
		// 値は空白も許さない。区切りの空白と値の中の空白を呼び出し元が区別できず、
		// key=value の対応が崩れるため(行全体の検査は区切りの空白を通すので、
		// この検査を省くと空白入りの値が素通りする)。
		if err := validateResultValue(*cursor); err != nil {
			return "", err
		}
		b.WriteString(" last_id=")
		b.WriteString(*cursor)
	}

	// 終端の LF を足す前に行全体を検証する。LF 自体は許容範囲外の制御文字だが、
	// 行の区切りとして必須であり、検証の対象は行の中身のほうである。
	line := b.String()
	if err := validateResultLine(line); err != nil {
		return "", err
	}
	return line + "\n", nil
}

// validateResultValue は結果行に載せる 1 つの値が空白・制御文字を含まない ASCII で
// あることを確かめる(要件 5.6 / FR-31)。許容範囲は 0x21〜0x7e。
func validateResultValue(value string) error {
	for i := range len(value) {
		if b := value[i]; b < 0x21 || b > 0x7e {
			return &ExitError{
				Code: ExitLocation,
				Err: fmt.Errorf("結果行に載せる値の %d バイト目が使えない文字(0x%02x)です。値は空白・制御文字を含まない ASCII である必要があります",
					i+1, b),
			}
		}
	}
	return nil
}

// validateResultLine は結果行(終端の改行を除く)が空白区切りとして解釈でき、
// ASCII の範囲に収まっていることを確かめる(要件 5.6 / FR-31、8.3 / FR-42)。
//
// 許容するのは 0x21〜0x7e と、key=value の区切りである空白(0x20)。
// 制御文字と非 ASCII はいずれも呼び出し元の結果行解析を壊すため拒否する。
func validateResultLine(line string) error {
	for i := range len(line) {
		if b := line[i]; b != ' ' && (b < 0x21 || b > 0x7e) {
			return &ExitError{
				Code: ExitLocation,
				Err: fmt.Errorf("結果行の %d バイト目が使えない文字(0x%02x)です。結果行は空白・制御文字を含まない ASCII である必要があります",
					i+1, b),
			}
		}
	}
	return nil
}

// reportError は人間向けメッセージを標準エラー出力へ書き、終了コードを返す
// (要件 7.4、8.2 / FR-41)。標準出力へは一切書かない(要件 8.1 / FR-40)。
//
// 終了コードの写像は *ExitError の Code だけを根拠とする。ExitError でないエラーは
// 各コンポーネントが正規化を漏らした場合にのみ現れるため、予期しない内部エラー
// (終了コード 9)として扱う。
func reportError(stderr io.Writer, err error) int {
	var ee *ExitError
	if !errors.As(err, &ee) {
		fmt.Fprintf(stderr, "%s予期しない内部エラー: %v\n", messagePrefix, err)
		return ExitInternal
	}

	fmt.Fprintf(stderr, "%s%v\n", messagePrefix, err)
	if ee.Code == ExitUsage {
		// 本ツールはヘルプが唯一の文書であるため(要件 11.2)、引数の誤りには
		// 参照先を示す。標準エラー出力なので結果行の解析には影響しない。
		fmt.Fprintf(stderr, "%s使い方は --help を参照してください。\n", messagePrefix)
	}
	return ee.Code
}
