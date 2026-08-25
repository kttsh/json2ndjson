package main

import (
	"fmt"
	"io"
	"os"
)

// main は os.Exit を呼び出す唯一の箇所(design.md「main(エントリ・終了コード集約)」、
// IMPL-19/IMPL-20)。実処理はテスト可能な run に委譲する。
func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run はテスト可能な実行本体で、終了コードを戻り値として返す。
//
// 本タスク(基盤整備)では意図的にスタブであり、実行フローの結線
// (引数解析 → 出力オープン → 変換 → カーソル抽出 → 確定 → 結果行出力、
// および defer による中断処理と panic からの回復)はタスク 5.1 で実装する。
// スタブの間は未実装であることを stderr へ通知し、予期しない内部エラーとして
// 終了コード 9 を返す。標準出力は結果行専用のため(要件 8.1、FR-40)、
// スタブは stdout へ何も書かない。
func run(args []string, stdout, stderr io.Writer) int {
	fmt.Fprintf(stderr, "%s: 実行フローは未実装です(タスク 5.1 で結線)\n", versionLine())
	return ExitInternal
}
