# プログラムの構造

## ファイル構成と責務

リポジトリ直下のフラットな `main` パッケージに、1 ファイル 1 責務で分離している。
単一バイナリの CLI に対する Go 公式レイアウトガイド([Organizing a Go module](https://go.dev/doc/modules/layout))の推奨に沿った構成であり、内部パッケージや `cmd/` は意図的に作っていない。

| ファイル | 責務 |
|---|---|
| `main.go` | 実行フロー統括。`os.Exit` の唯一の呼び出し箇所、結果行の出力 |
| `cli.go` | 引数解析(`flag`)と検証。検証済みの `Config` を生成する |
| `errors.go` | 終了コード付き error(`ExitError`)と終了コード定数 |
| `pointer.go` | RFC 6901 JSON Pointer の解析。他ファイルに依存しない |
| `convert.go` | ストリーミング変換コア(Decoder でポインタ位置へ移動し、1 レコードずつ処理) |
| `transform.go` | レコード単位の整形(重複排除、キー置換、フィールド追加、サイズ上限)とカーソル値抽出 |
| `output.go` | 出力ファイル管理。新規(temp+rename)、追記(ジャーナル+truncate)、復旧、確定 |
| `lock_windows.go` / `lock_other.go` | 排他ロック。Windows は `LockFileEx`、それ以外は開発用の no-op(ビルドタグで分離) |
| `version.go` | バージョン変数(ビルド時に注入)と `--version` / `--help` の文言 |

## 依存の方向

依存方向は一方向で、`main` から `cli` / `convert` / `output` を経て `pointer` / `lock` に至る。
下位のコンポーネントは失敗を `ExitError` として返すだけで、標準エラー出力への表示と `os.Exit` は `main.go` が 1 箇所で行う。

JSON 処理は標準ライブラリ `encoding/json/jsontext` の Decoder / Encoder によるストリーミングのみで行い、`encoding/json` v1 の `Unmarshal` は使わない。
レコード全体をメモリ上の汎用構造(`any`)へ展開しないことで、数値などの字句保持と一定メモリでの処理を両立している。
サードパーティ依存は Windows のファイルロックに使う `golang.org/x/sys` の 1 つだけである。

## テストの構成

テストは実装ファイルと同名の `*_test.go` に置き、`testdata/` の入力と期待値のペア(`<ケース名>.json` と `<ケース名>.ndjson`)を突き合わせるテーブル駆動が基本形。

| テスト | 担当 |
|---|---|
| `main_test.go` | 終了コードと結果行の E2E 検証、冪等性、パスの特殊文字 |
| `lexeme_test.go` | 数値字句の無変換、`\uXXXX` の非導入、サロゲートペアの保持 |
| `output_test.go` | 追記とロールバック、ジャーナル復旧、排他 |
| `perf_test.go` | 300 MB 級の時間とメモリの計測(`-short` でスキップ) |
| `fuzz_test.go` | JSON 入力のファジングハーネス(`FuzzRun`) |

## 仕様の正本

要件定義書は [requirements.md](requirements.md)、非機能テストの計画は [nonfunctional-test-plan.md](nonfunctional-test-plan.md) にある。
設計判断と実装タスクの記録は `.kiro/specs/json2ndjson/` 配下(design.md、tasks.md)にあり、挙動の根拠を調べるときはこれらを参照してほしい。
