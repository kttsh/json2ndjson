# json2ndjson

JSON ファイルを NDJSON(1 行 1 JSON 値)へ変換する非対話型 CLI。
DataSpider Servista 5.0.x の [外部アプリケーション起動] 処理から同期的に呼び出して使うことを想定している。

DataSpider には JSON ファイルを直接読み書きするアダプタがないため、REST アダプタが保存したレスポンス JSON を後続システムへ渡すには変換手段を外部に用意する必要がある。
本ツールは単一の実行ファイル `json2ndjson.exe` を配置するだけでその変換を担う。
追加のランタイムやライブラリは要らない。

## 特徴

- **ストリーミング変換**：入力サイズに依存しない一定メモリで変換する(300 MB 程度の入力を通常運用で処理)。変換対象の位置は JSON Pointer(RFC 6901)で指定する
- **字句保持**：数値などの値を生の字句のまま書き出し、丸めや再整形をしない
- **原子的な出力**：どの時点で失敗しても、出力ファイルは「新規作成なら存在しない、追記なら追記開始前のサイズ」に復元される。強制終了後もジャーナルにより次回実行時に復旧する
- **排他制御**：同じ出力ファイルへの同時実行はロックで検出し、終了コード 4 で停止する
- **ページングループ制御**：標準出力の結果行だけで、呼び出し側はレスポンス JSON を解釈せずにループを制御できる

## 使い方

### 基本形

```
json2ndjson --in <入力ファイル> --out <出力ファイル> [オプション]
```

配列を 1 行 1 レコードに展開する例。

```
json2ndjson --in page.json --out C:\data\out.ndjson --path /data/items
```

`--path` の位置が配列なら各要素を 1 レコード、オブジェクトならそれ自体を 1 レコードとして出力する。
省略時はルートが対象になる。

### 主な引数

| 引数 | 意味 |
|---|---|
| `--in <パス>` | 入力 JSON ファイル(UTF-8、BOM 可)。必須。複数回指定でき、ファイル名の昇順に処理する |
| `--out <パス>` | 出力 NDJSON ファイルの絶対パス。必須 |
| `--path <JSON Pointer>` | 変換対象の位置。省略時はルート |
| `--append` | 既存の出力ファイルの末尾へ追記する(なければ新規作成) |
| `--overwrite` | 既存の出力ファイルを上書きする(`--append` と排他) |
| `--newline lf\|crlf` | 行の改行コード。既定は `lf` |
| `--cursor-key <JSON Pointer>` | 最後に出力したレコードの指定位置の値を結果行の `last_id` に載せる |
| `--dedupe-key <JSON Pointer>` | 指定位置の値が同一のレコードを同一実行内で 1 回だけ出力する |
| `--version` / `--help` | バージョン / 使い方を表示して終了する |

このほかに整形用の引数(`--add-field`、`--key-hyphen-to-underscore`、`--max-record-bytes`、`--skip-oversize` など)がある。
全引数の説明と注意事項は `json2ndjson --help` が正本であり、そちらを参照してほしい。
設定はすべて引数で指定し、環境変数や設定ファイルは参照しない。

### 結果行と終了コード

標準出力は**結果行**の 1 行だけに使う(エラーメッセージは標準エラー出力へ出る)。

```
records=<出力件数> files=<処理ファイル数> [last_id=<値>]
```

`last_id` は `--cursor-key` 指定時のみ、かつ出力件数が 1 件以上のときに現れる。
呼び出し側はこの値を次ページの取得条件に渡すことでページングループを組める。

| 終了コード | 意味 |
|---|---|
| 0 | 正常終了 |
| 1 | 引数不正 |
| 2 | 入力ファイル不可(不存在、権限) |
| 3 | JSON 構文エラー |
| 4 | 出力書き込みエラー(権限、ディスク、排他、既存ファイル、ジャーナル破損) |
| 5 | 位置不正(`--path` / `--cursor-key` の位置、レコードのサイズ超過) |
| 9 | 予期しない内部エラー |

### ページングループでの典型的な呼び出し

API を 1 ページずつ取得し、ページごとに同じ出力ファイルへ追記していく使い方を想定している。

```
json2ndjson --in page_001.json --out C:\data\out.ndjson --path /data --append --cursor-key /id
```

1. 呼び出し側が API レスポンスをファイルに保存する
2. 本ツールを `--append` で呼び、結果行の `last_id` を受け取る
3. `last_id` を次ページの取得条件にしてループする(`records=0` になったら終了)

途中でどのページの変換が失敗しても、出力ファイルは追記開始前の状態に戻っているため、そのページからやり直せる。

## プログラムの構造

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

依存方向は一方向で、`main` から `cli` / `convert` / `output` を経て `pointer` / `lock` に至る。
下位のコンポーネントは失敗を `ExitError` として返すだけで、標準エラー出力への表示と `os.Exit` は `main.go` が 1 箇所で行う。

JSON 処理は標準ライブラリ `encoding/json/jsontext` の Decoder / Encoder によるストリーミングのみで行い、`encoding/json` v1 の `Unmarshal` は使わない。
サードパーティ依存は Windows のファイルロックに使う `golang.org/x/sys` の 1 つだけである。

テストは実装ファイルと同名の `*_test.go` に置き、`testdata/` の入力と期待値のペア(`<ケース名>.json` と `<ケース名>.ndjson`)を突き合わせるテーブル駆動が基本形。
`main_test.go` が終了コードと結果行の E2E 検証、`perf_test.go` が 300 MB 級の時間とメモリの計測を担う(後者は `-short` でスキップされる)。

## ビルド方法

### 前提

Go 1.27 系が必要である。
`go.mod` がバージョンをパッチまで固定しているため、`GOTOOLCHAIN=auto`(既定)の環境なら手元の Go が古くても該当ツールチェーンが自動取得される。

### 開発時のビルドとテスト

```bash
go build .              # 手元 OS 向けのビルド
go test -short ./...    # 通常のテスト(300 MB 級の性能テストを除外)
go test ./...           # 性能・メモリ計測を含む全テスト
```

### Windows 向け配布ビルド

配布用の `json2ndjson.exe` はスクリプトで生成する。

```bash
scripts/build-windows.sh              # バージョンは git タグから解決(なければ dev)
scripts/build-windows.sh v1.2.3       # バージョンを明示
```

スクリプトは gofmt / go vet / staticcheck / テストの検証を通したうえで、`GOOS=windows GOARCH=amd64 CGO_ENABLED=0` でクロスコンパイルし、SHA-256 チェックサムとともに `dist/` へ出力する。
ビルドはどの OS 上でも行えるが、配布前の受け入れテストは Windows Server 上で実施する。

バージョンはビルド時に `-ldflags "-X main.version=<タグ>"` で埋め込まれ、`--version` で確認できる。

## 仕様の正本

要件定義書は `docs/requirements.md`、設計や実装タスクの記録は `.kiro/specs/json2ndjson/` にある。
挙動の根拠を調べるときはこれらを参照してほしい。
