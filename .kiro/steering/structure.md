# Project Structure

## Organization Philosophy

リポジトリ直下のフラットな `main` パッケージに、**ファイル単位で責務を分離**する(IMPL-00)。内部パッケージ・`cmd/`・`pkg/` は作らない。これは単一バイナリ CLI に対する Go 公式レイアウトガイド(go.dev/doc/modules/layout)の推奨形であり、意図的な設計判断である。新しい責務は「新しいファイル」として追加し、ディレクトリは増やさない。

## Directory Patterns

### 実装ファイル(リポジトリ直下)
1 ファイル = 1 責務。現在の責務マップ:
- `main.go` — 実行フロー統括。**`os.Exit` の唯一の呼び出し箇所**、結果行の出力
- `cli.go` — 引数解析(`flag`)→ `Config` 構造体
- `errors.go` — `ExitError` 型(終了コード付き error)と終了コード定数
- `pointer.go` — RFC 6901 JSON Pointer。**他ファイルに依存しない**
- `convert.go` — ストリーミング変換コア(Decoder → レコード → Encoder)
- `transform.go` — レコード単位の整形とカーソル値抽出
- `output.go` — 出力ファイル管理(新規/追記/復旧/確定/ロールバック)
- `version.go` — バージョン変数と `--version`/`--help` 文言

### OS 固有コード
**Pattern**: `<名前>_windows.go`(`//go:build windows`)+ `<名前>_other.go`(`//go:build !windows`)のペア。非 Windows 側は開発環境でテストを通すための no-op 実装。
**Example**: `lock_windows.go` / `lock_other.go`

### テスト
**Location**: 実装ファイルと同名の `<名前>_test.go` + `testdata/`
**Pattern**:
- テーブル駆動テスト。フィクスチャは `testdata/<ケース名>.json` →期待値 `testdata/<ケース名>.ndjson` のゴールデンペア
- 異常系入力は `testdata/bad_*.json`(期待値ファイルなし、エラーを検証)
- `main_test.go` は E2E 相当(プロセス内実行で終了コードと結果行を検証)、`perf_test.go` は `testing.Short()` でスキップ

### その他
- `scripts/` — ビルド・運用スクリプト(bash、`set -euo pipefail`、ログは日本語)
- `docs/` — 要件定義書などの正本ドキュメント
- `.kiro/specs/json2ndjson/` — スペック(requirements/design/tasks)。実装判断の根拠は IMPL-xx / FR-xx / NFR-xx の ID で参照する

## Naming Conventions

- **ファイル**: 小文字 1 単語(`convert.go`)。OS 固有はサフィックス(`_windows`/`_other`)
- **エラー**: 終了コードを伴う失敗は `ExitError` に包む。エラーメッセージは日本語で「何が・どのパスで」を含める(例: `追記先 %q を開けない: %w`)
- **コメント**: 設計上の制約や「単純化してはならない理由」を、要件 ID(FR-xx 等)を添えて記録する

## Code Organization Principles

- 依存方向は一方向: `main` → `cli`/`convert`/`output` → `pointer`/`lock`。`pointer.go` は葉(依存なし)
- `os.Exit` は `main.go` 以外から呼ばない。下位層は `ExitError` を返して集約させる
- ビルド成果物(`/json2ndjson`、`/json2ndjson.exe`、`/dist/`)はリポジトリ直下に生成され `.gitignore` 済み

---
_Document patterns, not file trees. New files following patterns shouldn't require updates_
