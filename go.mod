// json2ndjson — DataSpider から呼び出される JSON → NDJSON 変換 CLI。
//
// 【go ディレクティブのバージョンについて】
// design.md「Technology Stack」(IMPL-01)の定めどおり Go 1.27 系最新パッチに固定する
// (2026-08-26 時点の最新は 1.27.0)。go ディレクティブがパッチまで含む完全な
// バージョンであるため、GOTOOLCHAIN=auto の環境では 1.27.0 のツールチェーンが
// 自動取得され、別途 toolchain ディレクティブを置く必要はない(同値の toolchain 行は
// go mod tidy が削除するため意図的に置かない)。
// Go 1.27 では jsonv2 が既定有効のため、`encoding/json/jsontext` の import に
// GOEXPERIMENT の設定は不要。
// (補足: 過去にツールチェーンのダウンロードが遮断される環境を理由に go 1.25.1 +
// GOEXPERIMENT=jsonv2 で運用していたが、現環境では 1.27.0 の取得を実測確認済み。)
//
// 【サードパーティ依存について】
// Windows のファイルロック(LockFileEx)用の golang.org/x/sys を唯一の依存とする(IMPL-02)。
// タスク 3.1 で lock_windows.go(//go:build windows)を追加したことにより require が確定した。
// import が Windows 向けビルドにしか現れないため直接依存に見えないおそれがあるが、
// `go mod tidy` は全ビルド構成を考慮するため直接依存として保持される(実測確認済み)。
// これ以外のサードパーティモジュールは追加しない。
module github.com/kttsh/json2ndjson

go 1.25.1

require golang.org/x/sys v0.47.0
