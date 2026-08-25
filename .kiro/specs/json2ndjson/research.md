# Research & Design Decisions: json2ndjson

## Summary

- **Feature**: `json2ndjson`
- **Discovery Scope**: New Feature(グリーンフィールド、フル・ディスカバリ)
- **Key Findings**:
  - `encoding/json/jsontext` は Go 1.27 で標準ライブラリとして既定有効(GOEXPERIMENT 不要)。Decoder/Encoder/Value の API 名は要件定義書 12 章の想定どおり存在する
  - `Encoder.WriteValue` は値を検証・再フォーマットするため「完全無変換」の保証には使えない。無変換パスは `Value.Compact()` 後の生バイト列を直接書き出す方式とし、Q-09 を設計レベルで解決した
  - `jsontext.SyntacticError` の位置情報はバイトオフセット+JSON Pointer(行・列なし)。FR-38 は「行、列またはバイトオフセット」を許容するため充足できる
  - `jsontext` の内蔵ネスト上限は 10000 固定(オプション変更不可)。IMPL-11 の上限 512 は自前チェックが必要

## Research Log

### Go 1.27 における encoding/json/jsontext の位置付け

- **Context**: 技術スタック決定(Go 1.27)の前提である「標準ライブラリでストリーミング読み取りと生字句保持」が本当に成立するかの確認
- **Sources Consulted**: pkg.go.dev/encoding/json/jsontext、pkg.go.dev/encoding/json/v2、golang/go#79788、Go 1.27 紹介記事(go.dev 本体は本環境から取得不可のため二次情報で裏取り)
- **Findings**:
  - Go 1.25 で `GOEXPERIMENT=jsonv2` として導入され、Go 1.27 で既定有効(オプトアウトは `GOEXPERIMENT=nojsonv2`)
  - 既定の準拠モードは RFC 7493(重複キー・不正 UTF-8 を拒否)。`AllowDuplicateNames(true)` / `AllowInvalidUTF8(true)` で緩和できる(IMPL-07 のとおり)
  - v1 `encoding/json` は内部的に v2 上に再実装されたが、v1 の意味論は維持
- **Implications**: サードパーティ依存なしのストリーミング変換という前提は成立。IMPL-05〜08 の API 前提も有効

### jsontext API の検証(Decoder/Encoder/Value/Options)

- **Context**: IMPL-05〜11 が名指しする API の実在と正確な意味の確認
- **Sources Consulted**: pkg.go.dev/encoding/json/jsontext、go-json-experiment/json の jsontext/state.go ソース
- **Findings**:
  - Decoder: `ReadToken()`, `ReadValue()`, `PeekKind()`, `SkipValue()`, `StackDepth()`, `InputOffset()`, `StackPointer()` すべて存在。構造の判定は `PeekKind()` をバイトリテラル(`'['`, `'{'`)と比較するのが文書化された方法
  - Encoder: `WriteValue(Value)`, `WriteToken(Token)`。**公式ドキュメントの明記**: 「Encoder は与えられた値をそのままコピーせず、構文検証したうえで Encoder の空白・文字列設定に従って再フォーマットする」
  - `PreserveRawStrings(true)`(Encoder 側オプション)で入力中のエスケープ済みシーケンス(`\uXXXX` 等)が出力に保持される
  - 数値の生字句は既定で保持される(`CanonicalizeRawInts`/`CanonicalizeRawFloats` を設定しない限り正規化されない)
  - `Value` は `[]byte` の別名型。`(*Value).Compact()` はインプレースで空白を除去
  - `SyntacticError` は `ByteOffset int64` と `JSONPointer Pointer` を持つ。**行・列は持たない**
  - ネスト上限は `maxNestingDepth = 10000` のハードコード定数(スタックオーバーフロー防止)。オプションで変更不可
- **Implications**:
  - 無変換パス(FR-16〜18)は Encoder を経由せず、`ReadValue` → `Compact` → 生バイト直接書き込みとする(下記 Design Decision 参照)
  - 整形パス(FR-33/34)の Encoder には `PreserveRawStrings(true)` を設定し、`EscapeForHTML`/`EscapeForJS` は既定(無効)のままとする
  - FR-38 のエラー位置はファイル名+バイトオフセット+JSON Pointer で表現する
  - IMPL-11 の深さ上限 512 は自前実装が必要(検証済みレコード生バイトの線形走査)

### Windows ファイルロック(golang.org/x/sys/windows)

- **Context**: IMPL-13 の `LockFileEx` 非ブロッキング排他ロックの API 確認
- **Sources Consulted**: golang/sys の syscall_windows.go、gofrs/flock の flock_windows.go(実績ある利用パターン)
- **Findings**:
  - `LockFileEx(file Handle, flags uint32, reserved uint32, bytesLow, bytesHigh uint32, overlapped *Overlapped) error`。フラグ `LOCKFILE_EXCLUSIVE_LOCK (0x2) | LOCKFILE_FAIL_IMMEDIATELY (0x1)` で非ブロッキング排他
  - ロック済みの場合は `ERROR_LOCK_VIOLATION` が返る(`errors.Is` で判定)。解放は同じバイト範囲で `UnlockFileEx`
- **Implications**: lock コンポーネントの契約(`tryLock`/`unlock`)は要件どおり実装可能。非 Windows は no-op(IMPL-16)

### os.Rename の Windows 挙動(原子的置換)

- **Context**: IMPL-12 の「一時ファイル+rename による原子的置換」が Windows で成立するかの確認
- **Sources Consulted**: golang/go commit 92c5736(#8914)、golang/go#78072
- **Findings**:
  - Go 1.5 以降、Windows の `os.Rename` は `MoveFileEx(MOVEFILE_REPLACE_EXISTING)` を呼び、既存の宛先ファイルを置換する
  - 同一 NTFS ボリューム内では実用上原子的。`MOVEFILE_COPY_ALLOWED` は付かないため、ボリュームをまたぐ rename は失敗する(コピー+削除に劣化しない)
- **Implications**: 一時ファイルは出力先と同一ディレクトリに置く(NFR-13 と一致)。ボリュームまたぎは終了コード 4 として素直に報告される

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Notes |
|--------|-------------|-----------|---------------------|-------|
| 単一パッケージ・パイプライン(採用) | `main` パッケージ 1 つ、ファイル単位の責務分離、依存方向を規約で固定 | ツール規模(推定 1,500〜2,500 行)に適合。IMPL-00(main はリポジトリ直下)と自然に整合。ビルド・テストが単純 | パッケージ境界による import 強制がない(レビューで担保) | 依存方向: pointer → transform → convert → output → main |
| internal/ パッケージ分割 | pointer/convert/output を internal パッケージ化 | import 方向をコンパイラが強制 | 本規模では過剰。ファイル数・ボイラープレート増 | Simplification レンズで却下 |
| ライブラリ+cmd 構成 | 変換コアを再利用可能ライブラリ化 | 将来の再利用 | 現要件に再利用シナリオがない。憶測的抽象化 | 却下(YAGNI) |

## Design Decisions

### Decision: 無変換パスは Encoder を経由せず生バイトを直接書く

- **Context**: FR-16〜18(値の完全無変換、数値字句保持、`\uXXXX` 非導入)と Q-09(`Encoder.WriteValue` がエスケープを書き直すか)
- **Alternatives Considered**:
  1. IMPL-06 の字義どおり `Compact` → `Encoder.WriteValue` — 公式ドキュメントにより WriteValue は文字列を Encoder 設定に従い再フォーマットすると判明。`PreserveRawStrings(true)` を付ければ保持されるが、保証が Encoder 設定の網羅に依存する
  2. `ReadValue` → `(*Value).Compact()` → `bufio.Writer` へ生バイト+改行を直接書く — Encoder の再フォーマット経路を完全に回避
- **Selected Approach**: 整形オプション(FR-33/34)が無効な既定経路では代替案 2 を採用。整形パスのみ Decoder→Encoder のトークン再エンコードを使い、`PreserveRawStrings(true)` を設定する
- **Rationale**: 「1 バイトも変えない」ことをコード構造で保証でき、jsontext の将来の既定変更の影響を受けない。Compact は構文検証済み Value に対する空白除去のみで、FR-10/11 に必要十分
- **Trade-offs**: IMPL-06 の字面(「WriteValue で書く」)から逸脱するが、その意図(コンパクト出力・HTML/JS エスケープ無効)はより強い形で満たす
- **Follow-up**: Q-07/Q-09 の PoC ケース(数値字句、`\uXXXX` 保持)を常設のテーブル駆動テストとして実装初期に置く

### Decision: ネスト深さ上限 512 は生バイトの線形走査で自前実装

- **Context**: IMPL-11(上限 512、超過は終了コード 3)に対し、jsontext の内蔵上限は 10000 固定で変更不可
- **Alternatives Considered**:
  1. jsontext の 10000 に任せ 512 を諦める — IMPL-11 違反
  2. レコードをトークン単位で読み深さを追跡 — 無変換パスの生 Value 取得と両立しない
  3. `ReadValue` で取得済み(=構文検証済み)のレコード生バイトを線形走査し、文字列外の `{`/`[` の深さを数える — O(n) のバイト走査のみ
- **Selected Approach**: 代替案 3。ポインタ移動中は `Decoder.StackDepth()` で監視し、レコード内部は取得後の線形走査で判定
- **Rationale**: 無変換パスを保ったまま IMPL-11 を満たす。jsontext の 10000 上限は 512 チェック以前のスタック保護として二重の安全網になる(NFR-08)
- **Trade-offs**: レコードごとに 1 回の追加走査(メモリ増なし、CPU は僅少)
- **Follow-up**: 深さ 511/512/513 の境界テストを 9.1/10.2 系テストに含める

### Decision: 排他方式 — 追記は出力ファイル自身、新規は決定的一時ファイルのロック

- **Context**: FR-25(同一出力への同時実行排他)と IMPL-13(追記時は出力ファイル自身をロック)。新規モードは rename で出力パスのファイル実体が置き換わるため、出力ファイル自身へのロックが成立しない
- **Alternatives Considered**:
  1. 両モードでサイドカーロックファイル `<出力名>.lock` — IMPL-13 から逸脱し、掃除すべきファイルが増える
  2. 追記=出力ファイル自身をロック、新規=一時ファイル名を `<出力名>.tmp` に固定して排他的作成+ロック — IMPL-13 準拠のまま新規モードにも排他を拡張
- **Selected Approach**: 代替案 2。新規モードで既存の `<出力名>.tmp` を検出した場合はロック探査を行い、ロック中なら終了コード 4(並行実行)、未ロックなら stale として削除・再作成する
- **Rationale**: 追記は IMPL-13 の字義どおり。新規はファイル数を増やさず(tmp は元々必要)、決定的な名前により同時実行が必ず衝突する
- **Trade-offs**: 一時ファイル名が固定になるため同一出力への意図的な並行新規作成は不可(要件上むしろ正しい)
- **Follow-up**: stale tmp の削除は排他確保後にのみ行うこと(誤って稼働中プロセスの tmp を消さない)をテストで固定

### Decision: ジャーナルは追記モード専用、復旧はロック取得後にのみ実施

- **Context**: NFR-07(強制終了後の復旧)と IMPL-14(ジャーナル `<出力名>.journal` に開始前サイズを記録)
- **Selected Approach**: ジャーナルは追記モードでのみ作成する。内容は `size=<10進数>`+LF の ASCII 1 行。追記の 1 バイト目より前に作成+`File.Sync` する。次回起動時、ロック取得後にジャーナルが残っていれば記録サイズへ `Truncate` してから削除し、通常処理へ進む。解釈不能な(破損した)ジャーナルは自動判断せず終了コード 4 で停止する
- **Rationale**: 「ジャーナル存在=未確定の追記がある」という単一の不変条件に還元でき、任意時点の強制終了に対して復旧が成立する。新規モードは temp+rename だけで FR-21 を満たすためジャーナル不要(ただし新規モードでも stale ジャーナルは、`--overwrite` による置換成功時に削除する — 置換後の追記実行が誤ったサイズへ切り詰めるのを防ぐ)
- **Trade-offs**: 追記のたびにジャーナルの作成・sync・削除の I/O が増えるが、ページあたり 3 回の小さな I/O であり NFR-03 に影響しない
- **Follow-up**: 「ジャーナル書き込み後・追記前」「追記中」「truncate 前」の各時点でのプロセス kill を模したテスト

### Decision: 要件が未規定のエッジケースの終了コード割り当て

- **Context**: 実装者間の解釈ぶれを防ぐため、要件に明示のないケースの扱いを設計で固定する
- **Selected Approach**:
  1. `--add-field` 指定時にレコードがオブジェクトでない場合 → 終了コード 1(FR-34 の衝突と同じ「オプションとデータの不整合」カテゴリ)
  2. `--cursor-key` の値が非スカラー(オブジェクト/配列)、または ASCII 範囲外・空白・制御文字を含む場合 → 終了コード 5(FR-31 の ASCII 制約を満たせない位置指定として扱う)。文字列値は引用符なしで内容のみ、数値は生字句を `last_id` に出す
  3. `--skip-oversize` を `--max-record-bytes` なしで単独指定 → 終了コード 1(依存関係の明示)
  4. `--in` 複数指定の整列キーは「ベース名の昇順(バイト比較)、同名時はフルパスで安定化」(FR-02 の「ファイル名の昇順」の解釈)
- **Rationale**: いずれも既存の終了コード体系(1=引数と入力の不整合、5=位置指定の不成立)の意味論に沿わせた
- **Follow-up**: ヘルプ(`--help`)にこれらの挙動を明記する

### Decision(synthesis 記録)

- **Generalization**: `--path`/`--cursor-key`/`--dedupe-key` はいずれも「JSON Pointer で指し示す」問題の変種。Pointer 型(解析)と extract(値取り出し)を単一の共通部品に一般化し、3 オプションすべてがこれを使う。実装は解析と抽出のみに留め、汎用 JSON Pointer ライブラリ化はしない
- **Build vs. Adopt**: JSON 処理は標準ライブラリ jsontext を採用(検証済み)。JSON Pointer は標準ライブラリに実装がなく、サードパーティ導入は IMPL-02 違反のため自前実装(RFC 6901 は 3 ページの小さな仕様であり妥当)。ファイルロックは x/sys/windows を採用(要件が明示する唯一の例外依存)
- **Simplification**: internal パッケージ分割・変換コアのライブラリ化・設定ファイル対応をすべて却下。RecordSink インターフェースは convert↔output の縦割り境界に必要な 1 本のみ残した

## Risks & Mitigations

- **jsontext の既定動作変更(Go 1.28 以降)** — go.mod の `go`/`toolchain` 固定(IMPL-01)でビルド環境差を排除。無変換パスは Encoder 非依存のため既定変更の影響面が小さい
- **`--dedupe-key` のキー集合によるメモリ超過(数百万件規模)** — NFR-05 に従い README とヘルプに上限の目安を文書化。dedupe は保険的な任意機能であり既定無効
- **ウイルス対策ソフトによる rename/ロックの一時失敗(Q-08)** — ツール側でリトライせず終了コード 4 で報告。冪等性(NFR-06)により呼び出し元の再実行が安全
- **DataSpider が取り込む stdout の文字コード未確認(Q-04)** — 結果行を ASCII に限定(FR-31)しているため、CP932/UTF-8 いずれでも結果行は化けない。stderr の日本語は Q-04 の結果次第で英語化を検討(Revalidation Trigger に登録)
- **Go 1.27 リリースノート原文の未検証** — go.dev が本環境から取得不可のため、jsontext 既定有効は複数の二次情報で裏取りした状態。実装初期の `go build` で即座に確定する(失敗すれば GOEXPERIMENT 指定で切り分け)

## References

- [encoding/json/jsontext](https://pkg.go.dev/encoding/json/jsontext) — Decoder/Encoder/Value/Options の API 検証
- [encoding/json/v2](https://pkg.go.dev/encoding/json/v2) — v2 の位置付け確認
- [golang/go#79788](https://github.com/golang/go/issues/79788) — GOEXPERIMENT=nojsonv2(オプトアウト)の扱い
- [go-json-experiment/json jsontext/state.go](https://github.com/go-json-experiment/json/blob/master/jsontext/state.go) — maxNestingDepth = 10000 の確認
- [golang/sys syscall_windows.go](https://github.com/golang/sys/blob/master/windows/syscall_windows.go) — LockFileEx/UnlockFileEx シグネチャ
- [gofrs/flock flock_windows.go](https://github.com/gofrs/flock/blob/master/flock_windows.go) — 非ブロッキング排他ロックの実装パターン
- [golang/go commit 92c5736](https://github.com/golang/go/commit/92c57363e0b4d193c4324e2af6902fe56b7524a0) — os.Rename の MOVEFILE_REPLACE_EXISTING 化(Go 1.5)
- [RFC 6901 JSON Pointer](https://www.rfc-editor.org/rfc/rfc6901) — 自前実装の準拠先
- [RFC 8259 JSON](https://www.rfc-editor.org/rfc/rfc8259) — 入力の準拠先
- `docs/requirements.md` 版 0.3 — 全 FR/NFR/IMPL/BUILD/TEST ID の出典
