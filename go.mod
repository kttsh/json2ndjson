// json2ndjson — DataSpider から呼び出される JSON → NDJSON 変換 CLI。
//
// 【go ディレクティブのバージョンについて(design.md からの意図的な逸脱)】
// design.md「Technology Stack」は Go 1.27 系を go/toolchain ディレクティブで固定すると
// 定めているが、本リポジトリのビルド環境では Go 1.27 のツールチェーンを取得できない
// (組織のエグレス制限によりツールチェーンのダウンロードが遮断される)。
// そのため go ディレクティブは「必要な最小バージョン」として 1.25.1 を宣言し、
// toolchain ディレクティブは意図的に置かない(1.27 を強制するとビルド不能になるため)。
//   - Go 1.27 以降でビルドする場合: jsonv2 が既定有効のため GOEXPERIMENT の設定は不要。
//     この go.mod を変更せずそのままビルドできる(go ディレクティブは最小バージョンのため)。
//   - Go 1.25.x でビルドする場合:  `GOEXPERIMENT=jsonv2` を設定した場合のみ
//     `encoding/json/jsontext` を import できる。
//
// 【サードパーティ依存について】
// Windows のファイルロック(LockFileEx)用の golang.org/x/sys を唯一の依存とする(IMPL-02)。
// 本タスクの時点では x/sys を import するソース(lock_windows.go)が存在しないため、
// ここに require を書いても `go mod tidy` が即座に削除して go.mod を書き換えてしまう。
// require の追加は lock_windows.go を作成するタスク 3.1 で行う。
module github.com/kttsh/json2ndjson

go 1.25.1
