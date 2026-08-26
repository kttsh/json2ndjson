package main

import "errors"

// ErrLocked は対象ファイルが別のプロセスによってすでに排他ロックされており、
// ロックを取得できなかったことを表す番兵エラー(要件 4.6 / FR-25)。
//
// 呼び出し側(output)は errors.Is(err, ErrLocked) で判別し、終了コード 4
// (ExitOutput)へ写像する。ここで ExitError を返さないのは、終了コードへの写像を
// 検出箇所ではなく呼び出し側の責務とする方針(design.md「Error Handling」の
// 検出箇所は output, lock、終了コードの割り当ては output)に揃えるためであり、
// ParsePointer が素の error を返すのと同じ扱いである。
//
// 【このファイルにビルドタグを置かない理由】
// ErrLocked は lock_windows.go(実ロック)と lock_other.go(no-op)の双方が
// 参照する必要がある。どちらか一方のファイルで宣言すると、もう一方の OS 向け
// ビルドで未定義になる。両方で宣言する手もあるが、メッセージや意味が将来
// 片方だけ変更されて乖離する余地が残るため、ビルドタグを持たない lock.go に
// 単一の宣言を置く。
var ErrLocked = errors.New("対象ファイルは別のプロセスが使用中(排他ロックを取得できない)")
