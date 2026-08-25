package main

// version はビルド時に -ldflags "-X main.version=<タグ>" で注入する
// バージョン文字列。未注入時は "dev"(IMPL-21、NFR-11)。
var version = "dev"

// versionLine は --version が標準出力へ表示する 1 行を返す
// (design.md「errors / version」: `json2ndjson <version>`)。
//
// --version / --help の引数としての受け付けと終了コード 0 での終了は
// タスク 4.1(引数解析)の責務。
func versionLine() string {
	return "json2ndjson " + version
}
