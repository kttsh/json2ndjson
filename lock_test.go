package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// design.md「lock(OS 別排他ロック)」の Service Interface の署名をコンパイル時に固定する。
// ビルドタグで分かれた 2 実装(lock_windows.go / lock_other.go)が同一署名であることは、
// Windows 版を実行できない開発環境では型検査でしか確認できない。
// このファイルはビルドタグを持たないため、`GOOS=windows go vet` / `go test -c` でも
// 同じ検査が働く。
var (
	_ func(*os.File) error = tryLock
	_ func(*os.File) error = unlock
)

// newLockTarget はロック対象のファイルを作成して開く。
// LockFileEx は読み取りまたは書き込みのアクセス権を要求するため O_RDWR で開く。
func newLockTarget(t *testing.T, dir, name string) *os.File {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatalf("ロック対象ファイルを開けない: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// TestTryLockThenUnlock は、誰もロックしていないファイルに対する取得と解放が
// どの OS でも成功することを検証する(要件 4.6、design.md「lock」)。
func TestTryLockThenUnlock(t *testing.T) {
	f := newLockTarget(t, t.TempDir(), "out.ndjson")

	if err := tryLock(f); err != nil {
		t.Fatalf("未ロックのファイルへの tryLock が失敗した: %v", err)
	}
	if err := unlock(f); err != nil {
		t.Fatalf("自身が取得したロックの unlock が失敗した: %v", err)
	}
}

// TestTryLockAfterUnlockSucceedsAgain は、解放したロックを再取得できることを検証する。
// 追記モード(タスク 3.3)は Finalize/Abort で unlock した後に同一パスを再実行するため、
// ロックが解放されずに残らないことが冪等性(要件 10.1)の前提になる。
func TestTryLockAfterUnlockSucceedsAgain(t *testing.T) {
	f := newLockTarget(t, t.TempDir(), "out.ndjson")

	if err := tryLock(f); err != nil {
		t.Fatalf("1 回目の tryLock が失敗した: %v", err)
	}
	if err := unlock(f); err != nil {
		t.Fatalf("unlock が失敗した: %v", err)
	}
	if err := tryLock(f); err != nil {
		t.Fatalf("解放後の再ロックが失敗した: %v", err)
	}
	if err := unlock(f); err != nil {
		t.Fatalf("2 回目の unlock が失敗した: %v", err)
	}
}

// TestErrLockedIsSentinel は ErrLocked が errors.Is で判別できる番兵エラーであり、
// 終了コードを内包しない素の error であることを検証する。
// 排他できない場合の終了コード 4(要件 4.6)への写像は呼び出し側(output)の責務であり、
// これは ParsePointer が素の error を返す方針(tasks.md「Implementation Notes」)と揃えている。
func TestErrLockedIsSentinel(t *testing.T) {
	// output はロック失敗にパス名を添えて包む。包んでも判別できなければ、
	// 呼び出し側は終了コード 4 と他の出力エラーを区別できない。
	wrapped := fmt.Errorf("出力ファイル %q をロックできない: %w", "C:\\out\\a.ndjson", ErrLocked)
	if !errors.Is(wrapped, ErrLocked) {
		t.Errorf("ラップした ErrLocked を errors.Is が検出できない: %v", wrapped)
	}

	// 無関係なエラーを ErrLocked と誤認しないこと。誤認すると書き込み権限エラーなどが
	// 「同時実行」として報告される。
	if errors.Is(errors.New("permission denied"), ErrLocked) {
		t.Error("無関係なエラーが ErrLocked と一致した")
	}

	// stderr には人間向けの理由を書く(要件 8.2)。メッセージが空だと理由が消える。
	if ErrLocked.Error() == "" {
		t.Error("ErrLocked のメッセージが空文字列")
	}

	// ErrLocked 自身は ExitError を含まない(終了コードの写像は output の責務)。
	var ee *ExitError
	if errors.As(ErrLocked, &ee) {
		t.Errorf("ErrLocked が ExitError を内包している: %v", ee)
	}
}

// TestTryLockIsNoOpOnNonWindows は、非 Windows 実装が「常に成功する no-op」である
// ことを明示的に固定する(design.md「lock」の非 Windows 方針、IMPL-16)。
//
// 重要: 非 Windows では同じファイルを別ハンドルから二重にロックできてしまう。
// すなわちこのプラットフォームでは要件 4.6 の排他は成立しておらず、
// JSON 処理と追記ロジックのテストを任意の OS で実行可能にするための割り切りである。
// ロック実体の検証は Windows 受け入れテストで担保する。
func TestTryLockIsNoOpOnNonWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows では実ロックのため二重取得は失敗する。ここでは no-op 実装のみを対象とする")
	}

	dir := t.TempDir()
	first := newLockTarget(t, dir, "out.ndjson")
	second := newLockTarget(t, dir, "out.ndjson") // 同一パスの別ハンドル

	if err := tryLock(first); err != nil {
		t.Fatalf("1 本目のハンドルの tryLock が失敗した: %v", err)
	}
	if err := tryLock(second); err != nil {
		t.Fatalf("no-op 実装なのに 2 本目のハンドルの tryLock が失敗した: %v", err)
	}

	if err := unlock(second); err != nil {
		t.Fatalf("2 本目のハンドルの unlock が失敗した: %v", err)
	}
	if err := unlock(first); err != nil {
		t.Fatalf("1 本目のハンドルの unlock が失敗した: %v", err)
	}
}

// TestUnlockWithoutLockIsNoOpOnNonWindows は、ロックを取得していないファイルへの
// unlock が非 Windows では成功することを検証する。
// output の Abort は「ロック取得前に失敗した場合」も defer で unlock を通る可能性があり、
// 非 Windows のテストがそこで落ちないことを保証する。
func TestUnlockWithoutLockIsNoOpOnNonWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows では未取得のロックの解放は失敗する")
	}

	f := newLockTarget(t, t.TempDir(), "out.ndjson")
	if err := unlock(f); err != nil {
		t.Fatalf("未ロックのファイルへの unlock が失敗した: %v", err)
	}
}
