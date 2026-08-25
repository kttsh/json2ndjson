package main

// Pointer は RFC 6901 JSON Pointer の参照トークン列(エスケープ解除済み)。
// 空スライスはルートを指す(design.md「pointer(RFC 6901 JSON Pointer)」)。
//
// Config が参照する型のため、型宣言のみを本タスク(基盤整備)で用意する。
// 文字列からの解析(ParsePointer)はタスク 2.1 の責務。
type Pointer []string
