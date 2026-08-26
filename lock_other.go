//go:build !windows

package main

import "os"

// 【非 Windows 実装は no-op であり、排他は成立しない】
//
// design.md「lock(OS 別排他ロック)」が定めるとおり、非 Windows では
// tryLock / unlock を常に成功する no-op とする(IMPL-16)。目的は、JSON 処理と
// 追記ロジックのテストを配布先(Windows)以外の開発環境でも実行可能にすることだけで、
// 要件 4.6 の同時実行排他をこのプラットフォームで満たすものではない。
// ロック実体の検証は Windows 受け入れテストで担保する。
//
// したがって非 Windows では、同一ファイルに対する二重の tryLock も、
// 取得していないロックの unlock も成功する。この挙動は lock_test.go の
// TestTryLockIsNoOpOnNonWindows / TestUnlockWithoutLockIsNoOpOnNonWindows で
// 明示的に固定してある。

// tryLock は f に排他ロックを試み、取得できなければ ErrLocked を返す(ブロックしない)。
// 非 Windows では何もせず常に nil を返す。
func tryLock(f *os.File) error {
	_ = f
	return nil
}

// unlock は tryLock で取得したロックを解放する。
// 非 Windows では何もせず常に nil を返す。
func unlock(f *os.File) error {
	_ = f
	return nil
}
