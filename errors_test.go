package main

import (
	"errors"
	"fmt"
	"testing"
)

// TestExitCodeConstants は終了コード体系(要件 7.4、design.md「Error Categories and
// Responses」)の各定数が規定の値であることを検証する。
func TestExitCodeConstants(t *testing.T) {
	tests := []struct {
		name string
		got  int
		want int
	}{
		{"正常終了", ExitOK, 0},
		{"引数不正", ExitUsage, 1},
		{"入力ファイル不可", ExitInput, 2},
		{"JSON 構文エラー", ExitSyntax, 3},
		{"出力書き込みエラー", ExitOutput, 4},
		{"位置不正", ExitLocation, 5},
		{"予期しない内部エラー", ExitInternal, 9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("終了コード = %d, want %d", tt.got, tt.want)
			}
		})
	}
}

// TestExitCodeConstantsAreDistinct は終了コードが互いに重複しないことを検証する。
// 重複すると呼び出し元(DataSpider)が失敗理由を区別できなくなる(要件 7.4)。
func TestExitCodeConstantsAreDistinct(t *testing.T) {
	codes := map[int]string{}
	for name, code := range map[string]int{
		"ExitOK":       ExitOK,
		"ExitUsage":    ExitUsage,
		"ExitInput":    ExitInput,
		"ExitSyntax":   ExitSyntax,
		"ExitOutput":   ExitOutput,
		"ExitLocation": ExitLocation,
		"ExitInternal": ExitInternal,
	} {
		if other, dup := codes[code]; dup {
			t.Errorf("終了コード %d が %s と %s で重複している", code, other, name)
			continue
		}
		codes[code] = name
	}
}

// TestExitErrorErrorMessage は Error() が原因エラーのメッセージをそのまま返すことを
// 検証する。stderr には人間向けメッセージのみを書くため(要件 8.2、FR-41)、
// 終了コードの数値をメッセージへ混ぜない。
func TestExitErrorErrorMessage(t *testing.T) {
	cause := errors.New("入力ファイルを開けません: page.json")
	err := &ExitError{Code: ExitInput, Err: cause}
	if got, want := err.Error(), cause.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// TestExitErrorErrorMessageWithoutCause は原因エラーを持たない場合でも Error() が
// 空文字列にならない(error 契約を満たす)ことを検証する。
func TestExitErrorErrorMessageWithoutCause(t *testing.T) {
	err := &ExitError{Code: ExitOutput}
	if got := err.Error(); got == "" {
		t.Error("Error() が空文字列を返した")
	}
}

// TestExitErrorUnwrap は Unwrap() が原因エラーを返し、errors.Is で原因まで
// たどれることを検証する。
func TestExitErrorUnwrap(t *testing.T) {
	cause := errors.New("ディスクに空きがありません")
	err := &ExitError{Code: ExitOutput, Err: cause}

	if got := err.Unwrap(); got != cause {
		t.Errorf("Unwrap() = %v, want %v", got, cause)
	}
	if !errors.Is(err, cause) {
		t.Errorf("errors.Is(err, cause) = false, want true")
	}

	empty := &ExitError{Code: ExitInternal}
	if got := empty.Unwrap(); got != nil {
		t.Errorf("Unwrap() = %v, want nil", got)
	}
}

// TestExitErrorAsThroughWrapping は fmt.Errorf("%w", ...) で包んだ後でも
// errors.As で終了コードを取り出せることを検証する
// (design.md「errors / version」: errors.As で main が code を取り出す)。
func TestExitErrorAsThroughWrapping(t *testing.T) {
	tests := []struct {
		name string
		code int
	}{
		{"引数不正", ExitUsage},
		{"JSON 構文エラー", ExitSyntax},
		{"位置不正", ExitLocation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := &ExitError{Code: tt.code, Err: errors.New("原因")}
			wrapped := fmt.Errorf("変換に失敗しました: %w", base)
			wrapped = fmt.Errorf("処理を中断します: %w", wrapped)

			var target *ExitError
			if !errors.As(wrapped, &target) {
				t.Fatalf("errors.As が *ExitError を取り出せなかった: %v", wrapped)
			}
			if target.Code != tt.code {
				t.Errorf("Code = %d, want %d", target.Code, tt.code)
			}
		})
	}
}

// TestExitErrorAsNotFound は ExitError を含まないエラーから errors.As が
// 取り出しに失敗する(=終了コード 9 へフォールバックできる)ことを検証する。
func TestExitErrorAsNotFound(t *testing.T) {
	var target *ExitError
	if errors.As(errors.New("ただのエラー"), &target) {
		t.Error("errors.As が *ExitError 以外から取り出しに成功した")
	}
}

// TestExitErrorImplementsError は *ExitError が error インターフェースを
// 満たすことを検証する。
func TestExitErrorImplementsError(t *testing.T) {
	var err error = &ExitError{Code: ExitUsage, Err: errors.New("x")}
	if err == nil {
		t.Fatal("*ExitError が error として扱えない")
	}
}
