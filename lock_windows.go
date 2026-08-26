//go:build windows

package main

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// ロック対象のバイト範囲。オフセット 0 から最大長(0xFFFFFFFFFFFFFFFF)を指定し、
// ファイル全体をロックする慣行に従う。LockFileEx はファイルの現在のサイズに関係なく
// 指定範囲をロックするため、追記でサイズが伸びても範囲を取り直す必要はない。
// tryLock と unlock で同一の範囲を指定しなければ解放できない。
const (
	lockRangeLengthLow  = ^uint32(0)
	lockRangeLengthHigh = ^uint32(0)
)

// withHandle は f の OS ハンドルを取り出して fn を呼ぶ。
//
// os.File.Fd() ではなく SyscallConn().Control を使うのは、Fd() が Windows で
// ファイルディスクリプタを Go ランタイムの I/O 完了ポートから切り離す副作用を持ち、
// また呼び出し中に f が GC / Close されないことを保証しないためである
// (os.File.Fd の doc コメントが SyscallConn の使用を推奨している)。
func withHandle(f *os.File, fn func(h windows.Handle) error) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var opErr error
	if err := rc.Control(func(fd uintptr) {
		opErr = fn(windows.Handle(fd))
	}); err != nil {
		return err
	}
	return opErr
}

// tryLock は f に排他ロックを試み、取得できなければ ErrLocked を返す(ブロックしない)。
//
// LOCKFILE_EXCLUSIVE_LOCK|LOCKFILE_FAIL_IMMEDIATELY を指定するため(IMPL-13)、
// 他プロセスが同じ範囲をロックしていても待たずに即座に失敗する。要件 4.6 の
// 「同時実行を排他し、排他できない場合は終了コード 4」を待ち時間なしで満たすための指定であり、
// ブロックさせると DataSpider 側のタイムアウトと競合する。
//
// 競合を示す ERROR_LOCK_VIOLATION(および重ね合わせハンドルで返りうる ERROR_IO_PENDING)
// のみを ErrLocked へ写像し、その他のエラー(ハンドル不正、アクセス権不足など)は
// そのまま返す。すべてを ErrLocked にまとめると、実際の障害が「同時実行」として
// 誤って報告される。
func tryLock(f *os.File) error {
	return withHandle(f, func(h windows.Handle) error {
		// Offset / OffsetHigh は 0 のまま(ファイル先頭からロックする)。
		var ol windows.Overlapped
		err := windows.LockFileEx(
			h,
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0, // reserved: 0 でなければならない
			lockRangeLengthLow,
			lockRangeLengthHigh,
			&ol,
		)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, windows.ERROR_LOCK_VIOLATION), errors.Is(err, windows.ERROR_IO_PENDING):
			return ErrLocked
		default:
			return err
		}
	})
}

// unlock は tryLock で取得したロックを解放する。
// 範囲は tryLock と完全に一致させる必要がある。
func unlock(f *os.File) error {
	return withHandle(f, func(h windows.Handle) error {
		var ol windows.Overlapped
		return windows.UnlockFileEx(
			h,
			0, // reserved: 0 でなければならない
			lockRangeLengthLow,
			lockRangeLengthHigh,
			&ol,
		)
	})
}
