# Product Overview

json2ndjson は、DataSpider Servista 5.0.x の [外部アプリケーション起動] 処理から同期的に呼び出される非対話型 CLI。REST アダプタが保存した JSON ファイル(最大 300 MB 程度)を NDJSON へ変換し、ファイルへ新規作成または追記する。DataSpider に JSON を直接扱うアダプタが存在しないという製品制約を、単一の実行ファイル `json2ndjson.exe` を配置するだけで埋める。

## Core Capabilities

- **ストリーミング変換**: 入力サイズに依存しない一定メモリで JSON → NDJSON 変換(配列 → 1 行 1 レコード、単一オブジェクト → 1 行)。変換対象位置は JSON Pointer(RFC 6901)で指定
- **字句保持**: 数値などの値を生バイト列のまま書き出し、丸め・再整形をしない(`jsontext.Value`)
- **原子的な出力**: 新規作成は temp+rename、追記はジャーナル+排他ロック。どの時点で失敗しても出力は「新規なら存在しない、追記なら追記開始前のサイズ」に復元される(強制終了後のジャーナル復旧を含む)
- **ページングループ制御**: 標準出力の結果行(`records`/`files`/`last_id`)により、DataSpider 側がレスポンス JSON を一切解釈せずにループを制御できる
- **終了コード体系**: 0〜5, 9 で失敗原因を区別し、DataSpider 側の分岐に使わせる

## Target Use Cases

- Coupa Core API などのページング API から DataSpider が 1 ページずつ取得した JSON を、ページごとに NDJSON へ追記していく(方式 A)
- 生成した NDJSON を後続システム(転送・BigQuery 等へのロード)へ渡す

## Value Proposition

- Mapper の文字列連結による手作り変換(エスケープ・型・ネストを自前処理)を置き換え、汎用性と保守性を確保する
- 追加ランタイム不要の単一 exe。サーバーへは配置するだけで動く
- 失敗時のデータ保護(ロールバック・復旧・排他)を変換ツール側で保証し、運用リトライを安全にする

## 責務境界

- REST API からの取得、変換後入力ファイルの削除、NDJSON の転送・ロードは **スコープ外**(DataSpider・後続システムの責務)
- 本ツールは 1 回の実行の一貫性のみ保証する。仕様の正本は `.kiro/specs/json2ndjson/` と `docs/requirements.md`

---
_Focus on patterns and purpose, not exhaustive feature lists_
