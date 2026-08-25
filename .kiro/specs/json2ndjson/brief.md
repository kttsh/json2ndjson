# Brief: json2ndjson

## Problem

DataSpider Servista 5.0.x には JSON ファイルを直接読み書きするアダプタが存在せず、REST API のレスポンスとして保存した JSON ファイルを NDJSON に変換する手段が純正機能にない。Mapper による文字列連結での手作り変換はエスケープ・型・ネスト処理をすべて自前で抱えることになり、汎用性と保守性に欠ける。この問題を抱えるのは、Coupa Core API などのページング API から取得したデータを NDJSON として後続システム(転送・ロード)へ渡す DataSpider スクリプトの開発・運用者である。

## Current State

- リポジトリは要件定義書(`docs/requirements.md` 版 0.3)のみのグリーンフィールド。実装は未着手。
- 技術スタックは Go 1.27 に決定済み(標準ライブラリ `encoding/json/jsontext` によるストリーミング読み取りと数値の生字句保持が決め手)。
- 公式 FAQ が示す代替策のうち「[外部アプリケーション起動] 処理で外部の変換ツールを呼ぶ」方式を採用することが決定済み。

## Desired Outcome

- 単一の実行ファイル `json2ndjson.exe` を配置するだけで、DataSpider の [外部アプリケーション起動] 処理から同期的に呼び出せる非対話型 CLI が動作する。
- JSON ファイル(最大 300 MB 程度)を入力サイズに依存しない一定メモリでストリーミング変換し、NDJSON をファイルへ新規作成または追記できる。
- 標準出力の `records`/`files`/`last_id` により、DataSpider 側がレスポンス JSON を一切解釈せずにページングループを制御できる。
- 失敗時は終了コード(1〜5, 9)で原因を区別でき、出力ファイルは新規作成なら「存在しない」、追記なら「元のサイズ」に復元される(強制終了後のジャーナル復旧を含む)。

## Approach

ページごとの変換と追記(方式 A): DataSpider が API を 1 ページずつ取得し、1 ページごとに変換ツールを呼んで出力ファイルへ追記する。全ページ保存後の一括変換(方式 B)は不採用。実装は Go 1.27 の標準ライブラリ(`encoding/json/jsontext` の Decoder/Encoder)で行い、サードパーティ依存は Windows ファイルロック用の `golang.org/x/sys/windows` のみとする。数値は `jsontext.Value` の生バイト列をそのまま書き出し、字句を保持する。選定理由・却下した代替案(JRE 流用、PowerShell、jq、GraalVM、.NET 10、Rust)は要件書 5.2 節に記録済み。

## Scope

- **In**:
  - CLI 引数解析(`--in`/`--out`/`--path`/`--append`/`--overwrite`/`--newline`/`--cursor-key` ほか、要件書 6.7 節の全引数)
  - JSON Pointer(RFC 6901)の自前実装による変換対象位置の指定
  - ストリーミング変換(配列 → 1 行 1 レコード、単一オブジェクト → 1 行)と字句保持(数値・値の無変換)
  - 出力ファイルの原子的新規作成、追記、ロールバック、ジャーナルによる強制終了後復旧、排他ロック
  - 標準出力への結果行(`records`/`files`/`last_id`)と終了コード体系(0〜5, 9)
  - 任意機能: `--dedupe-key`、`--key-hyphen-to-underscore`、`--add-field`、`--max-record-bytes`/`--skip-oversize`(優先度低、初版で省略可)
  - Windows 用 exe のクロスコンパイルビルド(BUILD-01)と、Windows 固有コードのビルドタグ分離(IMPL-16)
  - 9 章のテスト観点に基づくテーブル駆動テスト(TEST-01〜03)

- **Out**:
  - REST API からの取得(REST アダプタの設定)— DataSpider 側の責務
  - NDJSON の転送・ロード(BigQuery 等への投入)— 後続システムの責務
  - DataSpider スクリプト本体の実装 — 本スペックは呼び出し規約(要件書 8 章)を契約として参照するのみ
  - エラー応答の自動判定(FR-09)— HTTP ステータス判定は REST アダプタ側
  - ログファイルの出力(NFR-10)— 記録は DataSpider の実行ログに集約
  - DataSpider との結合テストの自動化(TEST-04)— 手順書とチェックリストで対応

## Boundary Candidates

- JSON Pointer の解釈(RFC 6901 準拠、自前実装)
- ストリーミング変換コア(Decoder → レコード単位の Value → Encoder)
- 出力ファイル管理(原子的リネーム、追記、ロールバック、ジャーナル、排他ロック)
- CLI 層(引数解析、終了コード集約、結果行出力)
- Windows 固有部分(LockFileEx)とクロスプラットフォーム no-op 実装の分離

## Out of Boundary

- API 取得条件(キーセットページング、差分取得窓)の設計 — 要件書 8.1 節の規約として参照するのみ
- 受け側システムの行長制限やキー名制約の確定(Q-06)— 既定値の調整材料として受け取る
- サーバーへの持ち込み承認・コード署名の手続き(Q-08)

## Upstream / Downstream

- **Upstream**: DataSpider Servista 5.0.x の [外部アプリケーション起動] 処理(呼び出し規約 5.3 節)、REST アダプタが保存するレスポンス JSON ファイル(UTF-8、BOM 有無不問)
- **Downstream**: DataSpider スクリプトのループ制御(標準出力の `records`/`last_id` を消費)、NDJSON を受け取る転送・ロード処理(BigQuery の JSON ロード等)

## Existing Spec Touchpoints

- **Extends**: なし(初のスペック)
- **Adjacent**: なし

## Constraints

- 実装言語は Go 1.27 系最新パッチ版。`go.mod` の `go`/`toolchain` ディレクティブでバージョン固定(IMPL-01)
- 標準ライブラリのみ。例外は `golang.org/x/sys/windows`(ファイルロック)だけ(IMPL-02)
- `encoding/json` v1 の `Unmarshal` や `any` への展開は禁止。`jsontext` の Decoder/Encoder でストリーミング処理(IMPL-05)
- `CGO_ENABLED=0`、単一 exe、追加ランタイム不要(IMPL-03、5.2 節)
- メモリ使用量は入力サイズ非依存、上限目安 256 MB(NFR-02)。300 MB 入力を通常運用で処理(NFR-03)
- 標準入力は使えない。入出力はファイルと引数のみ。対話・GUI 禁止(FR-36、5.3 節)
- 未決事項 Q-07(数値字句保持)と Q-09(`\uXXXX` エスケープの扱い)は実装初期の PoC で確認する
- 開発は任意 OS で可、Windows 用 exe はクロスコンパイル。受け入れテストは Windows Server 上(BUILD-02)
