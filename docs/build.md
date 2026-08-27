# ビルド方法

## 前提

Go 1.25.1 以上が必要で、配布ビルドは Go 1.27 系を正とする(設計 IMPL-01)。
`go.mod` の `go` ディレクティブは下限(1.25.1)を宣言している。
Go 1.27 以降では jsonv2 が既定で有効のため、追加設定なしでビルドできる。
Go 1.25.x では `GOEXPERIMENT=jsonv2` の設定が必要である(理由は `go.mod` 冒頭のコメントを参照)。

## 開発時のビルドとテスト

```bash
go build .              # 手元 OS 向けのビルド
go test -short ./...    # 通常のテスト(300 MB 級の性能テストを除外)
go test ./...           # 性能・メモリ計測を含む全テスト
```

このほかに、非機能の検証として次を任意で実行できる。

```bash
go test -race ./...                      # データ競合の検出
go test -fuzz FuzzRun -fuzztime 30m .    # JSON 入力のファジング
```

実施項目と合否基準は[非機能テスト計画書](nonfunctional-test-plan.md)にまとめてある。

## Windows 向け配布ビルド

配布用の `json2ndjson.exe` はスクリプトで生成する。

```bash
scripts/build-windows.sh              # バージョンは git タグから解決(なければ dev)
scripts/build-windows.sh v1.2.3       # バージョンを明示
```

スクリプトは gofmt / go vet / staticcheck / テストの検証を通したうえで、`GOOS=windows GOARCH=amd64 CGO_ENABLED=0` でクロスコンパイルし、SHA-256 チェックサムとともに `dist/` へ出力する。
生成物が Windows の PE 実行ファイルであること、バージョン文字列が埋め込まれていること、静的リンクであることまでを検証してから出力する。

ビルドはどの OS 上でも行えるが、配布前の受け入れテスト(要件定義書 9 章)は Windows Server 上で実施する。
排他ロックの実体(`LockFileEx`)は Windows でのみ働くため、多重実行と強制終了復旧の判定は実機でしか成立しない。

バージョンはビルド時に `-ldflags "-X main.version=<タグ>"` で埋め込まれ、`--version` で確認できる。
