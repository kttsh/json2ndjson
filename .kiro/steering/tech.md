# Technology Stack

## Architecture

単一バイナリの非対話型 CLI。リポジトリ直下の `main` パッケージのみで構成し、内部パッケージを作らない(IMPL-00)。入出力はファイルと引数のみで、標準入力・対話・GUI は使わない(FR-36)。

## Core Technologies

- **Language**: Go 1.27 系を配布ビルドの正とする(IMPL-01)。ただし `go.mod` の `go` ディレクティブは下限 1.25.1 を宣言する。開発コンテナのエグレスポリシーが 1.27 ツールチェーンの取得を遮断しており(2026-08-26 実測: proxy.golang.org / dl.google.com / go.dev すべて 403)、1.27 を要求すると開発環境でビルド不能になるため。Go 1.25.x では `GOEXPERIMENT=jsonv2` が必要
- **JSON 処理**: 標準ライブラリ `encoding/json/jsontext`(Go 1.27 で既定有効。GOEXPERIMENT 不要)
- **依存方針**: 標準ライブラリのみ。唯一の例外は Windows ファイルロック(`LockFileEx`)用の `golang.org/x/sys`(IMPL-02)。これ以外のサードパーティモジュールは追加しない

## Development Standards

### JSON 処理の制約(IMPL-05)
- `encoding/json` v1 の `Unmarshal` や `any` への展開は**禁止**。`jsontext` の Decoder/Encoder によるストリーミング処理のみ
- 数値・値は `jsontext.Value` の生バイト列をそのまま書き出し、字句を保持する
- メモリ使用量は入力サイズ非依存(上限目安 256 MB、300 MB 入力を通常運用で処理)

### Code Quality
- `gofmt` 差分ゼロ、`go vet` 通過が必須。`staticcheck` は開発環境のみのツールとして実行(IMPL-04)
- コメント・エラーメッセージ・ログは日本語で書く。「なぜそうするか」「なぜ単純化してはならないか」を制約として記録する(例: `output.go` の Windows open フラグの注釈)
- モダン Go イディオムを優先する(`errors.Is`、`min`/`max`、`slices`/`maps` ヘルパー、`b.Loop()` など)

### Testing
- テーブル駆動テスト + `testdata/` のゴールデンファイル比較が基本形(TEST-01)
- 300 MB 級の性能・メモリ計測は `testing.Short()` でスキップ可能にする(TEST-02/03)。通常の門番は `go test -short ./...`

## Development Environment

### Common Commands
```bash
go test -short ./...          # 通常のテスト(性能テスト除外)
go test ./...                 # 性能・メモリ計測を含む全テスト
scripts/build-windows.sh      # Windows 向け配布ビルド(検証 + SHA-256 付き)
```

配布ビルドの規定コマンド(BUILD-01):
```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=<タグ>"
```

## Key Technical Decisions

- **CGO_ENABLED=0 の単一 exe**: 追加ランタイム不要で「配置するだけで動く」を満たす(IMPL-03)。開発は任意 OS、Windows 向けはクロスコンパイル、受け入れテストのみ Windows Server 上(BUILD-02)
- **Windows 固有コードのビルドタグ分離**: `//go:build windows` / `//go:build !windows` のペアで、非 Windows では no-op 実装によりテスト可能にする(IMPL-16)
- **バージョン埋め込み**: `-ldflags -X main.version=<タグ>` で注入。未注入時の既定は `"dev"`(IMPL-21)
- **出力の原子性**: 新規は temp+`os.Rename`、追記はジャーナル(`<出力名>.journal`)+非ブロッキング排他ロック。一時ファイルは出力と同一ディレクトリに置く(同一ボリュームでの rename 保証、NFR-13)

---
_Document standards and patterns, not every dependency_
