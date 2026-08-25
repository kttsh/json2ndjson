# Requirements Document

## Project Description (Input)

DataSpider Servista 5.0.x には JSON ファイルを直接読み書きするアダプタが存在せず、REST API のレスポンスとして保存した JSON ファイルを NDJSON に変換する手段が純正機能にない。この問題を抱えるのは、Coupa Core API などのページング API から取得したデータを NDJSON として後続システム(転送・ロード)へ渡す DataSpider スクリプトの開発・運用者である。

現状、リポジトリには要件定義書(`docs/requirements.md` 版 0.3)のみが存在し、実装は未着手。技術スタックは Go 1.27(標準ライブラリ `encoding/json/jsontext` によるストリーミング処理)に決定済みで、DataSpider の [外部アプリケーション起動] 処理から外部変換ツールを呼ぶ方式を採用する。

単一の実行ファイル `json2ndjson.exe` を配置するだけで動作する非対話型 CLI を実装する。JSON ファイル(最大 300 MB 程度)を入力サイズに依存しない一定メモリでストリーミング変換し、NDJSON をファイルへ新規作成または追記する。標準出力の `records`/`files`/`last_id` により DataSpider 側がページングループを制御でき、失敗時は終了コード(1〜5, 9)で原因を区別し、出力ファイルを元の状態に復元する(強制終了後のジャーナル復旧を含む)。

詳細は `.kiro/specs/json2ndjson/brief.md` および `docs/requirements.md` を参照。

## Requirements
<!-- Will be generated in /kiro-spec-requirements phase -->
