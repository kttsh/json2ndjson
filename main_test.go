package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// main_test.go はタスク 5.1 が結線した実行フロー(run)を、引数から
// 終了コード・標準出力・標準エラー出力・出力ファイルの状態まで一気通貫で検証する
// (design.md「Testing Strategy / Integration Tests 5. E2E(run 関数)」)。
//
// 終了コード体系の網羅的な E2E マトリクスと運用制約(日本語・空白パス、冪等性)は
// タスク 5.2 の責務であり、ここでは 5.1 の完了条件に対応する観点だけを固定する。

// runCLI は run を呼び、終了コードと両ストリームの内容を返す。
func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	code = run(args, &out, &errBuf)
	return code, out.String(), errBuf.String()
}

// writeFile はテスト用の入力ファイルを作り、そのパスを返す。
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("テスト用ファイル %q を作成できません: %v", path, err)
	}
	return path
}

// panicOnceWriter は最初の Write だけ panic し、以降は buf へ書き出す io.Writer。
//
// run の panic 回復(要件 7.4 の終了コード 9)を、本番コードに専用のシームを
// 設けずに検証するために使う。回復後の報告そのものは 2 回目以降の Write として
// buf に残るため、報告内容も同時に検証できる。
type panicOnceWriter struct {
	panicked bool
	buf      bytes.Buffer
}

func (w *panicOnceWriter) Write(p []byte) (int, error) {
	if !w.panicked {
		w.panicked = true
		panic("テスト由来の意図的な panic")
	}
	return w.buf.Write(p)
}

// --- 表示要求(要件 7.5 / 7.6): parseArgs の番兵エラーの結線 ---

func TestRunVersionPrintsToStdout(t *testing.T) {
	code, stdout, stderr := runCLI(t, "--version")

	if code != ExitOK {
		t.Errorf("終了コード = %d, want %d", code, ExitOK)
	}
	if want := versionLine() + "\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if stderr != "" {
		t.Errorf("正常終了では stderr へ何も書かない(要件 8.2)。stderr = %q", stderr)
	}
}

func TestRunHelpPrintsToStdout(t *testing.T) {
	code, stdout, stderr := runCLI(t, "--help")

	if code != ExitOK {
		t.Errorf("終了コード = %d, want %d", code, ExitOK)
	}
	if want := usageText(); stdout != want {
		t.Errorf("stdout がヘルプ全文と一致しません。\n got %q\nwant %q", stdout, want)
	}
	if stderr != "" {
		t.Errorf("正常終了では stderr へ何も書かない(要件 8.2)。stderr = %q", stderr)
	}
}

// --version は他の検証より先に判定されるため、必須引数がなくても 0 で終わる。
func TestRunVersionIgnoresMissingRequiredArgs(t *testing.T) {
	code, stdout, _ := runCLI(t, "--version")
	if code != ExitOK || !strings.Contains(stdout, "json2ndjson") {
		t.Errorf("--version 単独で終了コード 0 とバージョン表示になりません: code=%d stdout=%q", code, stdout)
	}
}

// --- 正常系: 結果行 1 行だけが標準出力へ出る(要件 5.1、5.2、8.1) ---

func TestRunSuccessWritesSingleResultLine(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "in.json", `[{"id":1},{"id":2},{"id":3}]`)
	out := filepath.Join(dir, "out.ndjson")

	code, stdout, stderr := runCLI(t, "--in", in, "--out", out)

	if code != ExitOK {
		t.Fatalf("終了コード = %d, want %d (stderr=%q)", code, ExitOK, stderr)
	}
	if want := "records=3 files=1\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if strings.Count(stdout, "\n") != 1 {
		t.Errorf("標準出力は結果行 1 行のみでなければならない(要件 5.1 / 8.1)。stdout = %q", stdout)
	}
	if stderr != "" {
		t.Errorf("正常終了では stderr へ何も書かない(要件 8.2)。stderr = %q", stderr)
	}

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("出力ファイルを読めません: %v", err)
	}
	if want := "{\"id\":1}\n{\"id\":2}\n{\"id\":3}\n"; string(got) != want {
		t.Errorf("出力ファイル = %q, want %q", got, want)
	}
}

func TestRunResultLineIncludesLastID(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "in.json", `{"data":[{"id":"a1"},{"id":"a2"}]}`)
	out := filepath.Join(dir, "out.ndjson")

	code, stdout, stderr := runCLI(t, "--in", in, "--out", out, "--path", "/data", "--cursor-key", "/id")

	if code != ExitOK {
		t.Fatalf("終了コード = %d, want %d (stderr=%q)", code, ExitOK, stderr)
	}
	// 文字列は引用符を外した内容のみが載る(要件 5.3)。
	if want := "records=2 files=1 last_id=a2\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

// last_id は「最後に出力した」レコードから取る(要件 5.3)。
// 重複排除で落ちたレコードは出力ではないため、last_id の供給元にならない。
func TestRunLastIDComesFromLastEmittedRecord(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "in.json", `[{"id":"a"},{"id":"b"},{"id":"a"}]`)
	out := filepath.Join(dir, "out.ndjson")

	code, stdout, stderr := runCLI(t, "--in", in, "--out", out, "--cursor-key", "/id", "--dedupe-key", "/id")

	if code != ExitOK {
		t.Fatalf("終了コード = %d, want %d (stderr=%q)", code, ExitOK, stderr)
	}
	// 3 件目は重複で落ちるため records=2、最後に出力したのは 2 件目("b")。
	if want := "records=2 files=1 last_id=b\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

// --cursor-key 指定でも 0 件なら last_id を出さない(要件 5.4)。
func TestRunOmitsLastIDWhenNoRecords(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "in.json", `[]`)
	out := filepath.Join(dir, "out.ndjson")

	code, stdout, stderr := runCLI(t, "--in", in, "--out", out, "--cursor-key", "/id")

	if code != ExitOK {
		t.Fatalf("終了コード = %d, want %d (stderr=%q)", code, ExitOK, stderr)
	}
	if want := "records=0 files=1\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	// 空配列でも 0 バイトの出力ファイルを作る(要件 3.10)。
	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("出力ファイルが作られていません: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("出力ファイルのサイズ = %d, want 0", info.Size())
	}
}

func TestRunResultLineCountsAllInputFiles(t *testing.T) {
	dir := t.TempDir()
	a := writeFile(t, dir, "a.json", `[{"id":1}]`)
	b := writeFile(t, dir, "b.json", `[{"id":2}]`)
	out := filepath.Join(dir, "out.ndjson")

	code, stdout, stderr := runCLI(t, "--in", b, "--in", a, "--out", out, "--cursor-key", "/id")

	if code != ExitOK {
		t.Fatalf("終了コード = %d, want %d (stderr=%q)", code, ExitOK, stderr)
	}
	// 入力はベース名の昇順(a.json → b.json)で処理されるため last_id は 2。
	if want := "records=2 files=2 last_id=2\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

// --- 異常系: stdout は空、stderr にメッセージ、意図した終了コード ---

func TestRunErrorExitCodes(t *testing.T) {
	tests := []struct {
		name string
		// setup は入力ファイル群を用意し、run へ渡す引数を返す。
		setup    func(t *testing.T, dir string) []string
		wantCode int
	}{
		{
			name: "必須引数の欠落は引数不正",
			setup: func(t *testing.T, dir string) []string {
				in := writeFile(t, dir, "in.json", `[]`)
				return []string{"--in", in}
			},
			wantCode: ExitUsage,
		},
		{
			name: "未知の引数は引数不正",
			setup: func(t *testing.T, dir string) []string {
				in := writeFile(t, dir, "in.json", `[]`)
				return []string{"--in", in, "--out", filepath.Join(dir, "out.ndjson"), "--unknown"}
			},
			wantCode: ExitUsage,
		},
		{
			name: "--append と --overwrite の同時指定は引数不正",
			setup: func(t *testing.T, dir string) []string {
				in := writeFile(t, dir, "in.json", `[]`)
				return []string{"--in", in, "--out", filepath.Join(dir, "out.ndjson"), "--append", "--overwrite"}
			},
			wantCode: ExitUsage,
		},
		{
			name: "入力ファイルが存在しない",
			setup: func(t *testing.T, dir string) []string {
				return []string{"--in", filepath.Join(dir, "missing.json"), "--out", filepath.Join(dir, "out.ndjson")}
			},
			wantCode: ExitInput,
		},
		{
			name: "JSON 構文エラー",
			setup: func(t *testing.T, dir string) []string {
				in := writeFile(t, dir, "in.json", `[{"id":`)
				return []string{"--in", in, "--out", filepath.Join(dir, "out.ndjson")}
			},
			wantCode: ExitSyntax,
		},
		{
			name: "既存の出力ファイルと --overwrite なし",
			setup: func(t *testing.T, dir string) []string {
				in := writeFile(t, dir, "in.json", `[{"id":1}]`)
				out := writeFile(t, dir, "out.ndjson", "既存\n")
				return []string{"--in", in, "--out", out}
			},
			wantCode: ExitOutput,
		},
		{
			name: "--path の位置がスカラー",
			setup: func(t *testing.T, dir string) []string {
				in := writeFile(t, dir, "in.json", `{"data":1}`)
				return []string{"--in", in, "--out", filepath.Join(dir, "out.ndjson"), "--path", "/data"}
			},
			wantCode: ExitLocation,
		},
		{
			name: "--cursor-key の位置が最終レコードに存在しない",
			setup: func(t *testing.T, dir string) []string {
				in := writeFile(t, dir, "in.json", `[{"id":1}]`)
				return []string{"--in", in, "--out", filepath.Join(dir, "out.ndjson"), "--cursor-key", "/missing"}
			},
			wantCode: ExitLocation,
		},
		{
			name: "--max-record-bytes 超過(スキップなし)",
			setup: func(t *testing.T, dir string) []string {
				in := writeFile(t, dir, "in.json", `[{"id":1,"pad":"0123456789"}]`)
				return []string{"--in", in, "--out", filepath.Join(dir, "out.ndjson"), "--max-record-bytes", "8"}
			},
			wantCode: ExitLocation,
		},
		{
			name: "--add-field がレコードの既存キーと衝突",
			setup: func(t *testing.T, dir string) []string {
				in := writeFile(t, dir, "in.json", `[{"id":1}]`)
				return []string{"--in", in, "--out", filepath.Join(dir, "out.ndjson"), "--add-field", "id=x"}
			},
			wantCode: ExitUsage,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			args := tt.setup(t, dir)

			code, stdout, stderr := runCLI(t, args...)

			if code != tt.wantCode {
				t.Errorf("終了コード = %d, want %d (stderr=%q)", code, tt.wantCode, stderr)
			}
			if stdout != "" {
				t.Errorf("異常終了では標準出力へ何も書かない(要件 8.1)。stdout = %q", stdout)
			}
			if stderr == "" {
				t.Error("異常終了では標準エラー出力へ人間向けメッセージを書く(要件 8.2)")
			}
			if !strings.HasSuffix(stderr, "\n") {
				t.Errorf("stderr のメッセージは改行で終わるべきです。stderr = %q", stderr)
			}
		})
	}
}

// --- 異常終了時の出力ファイル復元(要件 4.9) ---

// 新規作成モードで途中失敗しても出力パスにファイルを残さない(要件 4.2 / 4.9)。
func TestRunNewModeLeavesNoOutputOnFailure(t *testing.T) {
	dir := t.TempDir()
	a := writeFile(t, dir, "a.json", `[{"id":1},{"id":2}]`)
	b := writeFile(t, dir, "b.json", `[{"id":`)
	out := filepath.Join(dir, "out.ndjson")

	code, stdout, stderr := runCLI(t, "--in", a, "--in", b, "--out", out)

	if code != ExitSyntax {
		t.Fatalf("終了コード = %d, want %d (stderr=%q)", code, ExitSyntax, stderr)
	}
	if stdout != "" {
		t.Errorf("異常終了では標準出力へ何も書かない(要件 8.1)。stdout = %q", stdout)
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Errorf("出力パスにファイルが残っています(要件 4.2 / 4.9): err = %v", err)
	}
	assertNoLeftovers(t, dir, out)
}

// 追記モードで途中失敗したら既存ファイルを開始前サイズへ戻す(要件 4.4 / 4.9)。
func TestRunAppendModeRestoresBaseSizeOnFailure(t *testing.T) {
	dir := t.TempDir()
	a := writeFile(t, dir, "a.json", `[{"id":1},{"id":2}]`)
	b := writeFile(t, dir, "b.json", `[{"id":`)
	const seed = "{\"id\":0}\n"
	out := writeFile(t, dir, "out.ndjson", seed)

	code, stdout, stderr := runCLI(t, "--in", a, "--in", b, "--out", out, "--append")

	if code != ExitSyntax {
		t.Fatalf("終了コード = %d, want %d (stderr=%q)", code, ExitSyntax, stderr)
	}
	if stdout != "" {
		t.Errorf("異常終了では標準出力へ何も書かない(要件 8.1)。stdout = %q", stdout)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("出力ファイルを読めません: %v", err)
	}
	if string(got) != seed {
		t.Errorf("出力ファイル = %q, want %q(追記開始前の状態へ戻すこと。要件 4.9)", got, seed)
	}
	assertNoLeftovers(t, dir, out)
}

// カーソル抽出の失敗は Finalize より前に検出し、出力を確定させない(design.md「System Flows」)。
func TestRunCursorFailureRollsBackAppend(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "in.json", `[{"id":1}]`)
	const seed = "{\"id\":0}\n"
	out := writeFile(t, dir, "out.ndjson", seed)

	code, stdout, stderr := runCLI(t, "--in", in, "--out", out, "--append", "--cursor-key", "/missing")

	if code != ExitLocation {
		t.Fatalf("終了コード = %d, want %d (stderr=%q)", code, ExitLocation, stderr)
	}
	if stdout != "" {
		t.Errorf("異常終了では標準出力へ何も書かない(要件 8.1)。stdout = %q", stdout)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("出力ファイルを読めません: %v", err)
	}
	if string(got) != seed {
		t.Errorf("出力ファイル = %q, want %q(カーソル抽出の失敗でも追記開始前へ戻すこと)", got, seed)
	}
	assertNoLeftovers(t, dir, out)
}

// 確定(Finalize)の段で失敗した場合も、結果行を出さず終了コードで報告する。
//
// 出力先をディレクトリにすると、一時ファイルへの書き込みは最後まで成功する一方で
// 最終段の rename が必ず失敗するため、「変換は終わったが確定できなかった」という
// 他の異常系では踏めない経路に到達できる。ここで結果行を出してしまうと、
// 呼び出し元は出力ファイルが存在しないのに成功したと解釈してループを進めてしまう。
func TestRunFinalizeFailureReportsWithoutResultLine(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "in.json", `[{"id":1}]`)
	// rename の置換先を空でないディレクトリにする(空ディレクトリは環境によって
	// rename が成功しうるため、必ず中身を置く)。
	out := filepath.Join(dir, "outdir")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatalf("テスト用ディレクトリを作成できません: %v", err)
	}
	writeFile(t, out, "keep.txt", "keep\n")

	code, stdout, stderr := runCLI(t, "--in", in, "--out", out, "--overwrite")

	if code != ExitOutput {
		t.Fatalf("終了コード = %d, want %d (stderr=%q)", code, ExitOutput, stderr)
	}
	if stdout != "" {
		t.Errorf("確定に失敗した実行が結果行を返してはいけない(要件 5.1 / 8.1)。stdout = %q", stdout)
	}
	if stderr == "" {
		t.Error("異常終了では標準エラー出力へ人間向けメッセージを書く(要件 8.2)")
	}
	// 確定に失敗しても一時ファイルは片付ける(要件 4.9)。
	assertNoLeftovers(t, dir, out)
	// 置換先の中身は無傷であること。
	if _, err := os.Stat(filepath.Join(out, "keep.txt")); err != nil {
		t.Errorf("既存の内容が失われています: %v", err)
	}
}

// assertNoLeftovers は一時ファイルとジャーナルが残っていないことを確かめる。
func assertNoLeftovers(t *testing.T, dir, out string) {
	t.Helper()
	for _, path := range []string{out + ".tmp", out + ".journal"} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("後始末されていないファイルが残っています: %q (err=%v)", path, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ディレクトリを読めません: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") || strings.HasSuffix(e.Name(), ".journal") {
			t.Errorf("後始末されていないファイルが残っています: %q", e.Name())
		}
	}
}

// --- 正常確定の後にだけ結果行を書く(要件 5.1 / FR-40) ---

// 追記モードの正常系。既存の内容を保ったまま追記し、結果行を 1 行だけ返す。
func TestRunAppendModeSucceeds(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "in.json", `[{"id":2}]`)
	// 末尾に改行がない既存ファイル。追記前に改行を補うこと(要件 4.5)。
	out := writeFile(t, dir, "out.ndjson", `{"id":1}`)

	code, stdout, stderr := runCLI(t, "--in", in, "--out", out, "--append", "--cursor-key", "/id")

	if code != ExitOK {
		t.Fatalf("終了コード = %d, want %d (stderr=%q)", code, ExitOK, stderr)
	}
	if want := "records=1 files=1 last_id=2\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("出力ファイルを読めません: %v", err)
	}
	if want := "{\"id\":1}\n{\"id\":2}\n"; string(got) != want {
		t.Errorf("出力ファイル = %q, want %q", got, want)
	}
	assertNoLeftovers(t, dir, out)
}

// --- 要件 6.6: --skip-oversize の警告が実際に標準エラー出力へ届く ---

func TestRunSkipOversizeWarnsToStderr(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "in.json", `[{"id":1},{"id":2,"pad":"0123456789"}]`)
	out := filepath.Join(dir, "out.ndjson")

	code, stdout, stderr := runCLI(t,
		"--in", in, "--out", out, "--max-record-bytes", "10", "--skip-oversize", "--cursor-key", "/id")

	if code != ExitOK {
		t.Fatalf("終了コード = %d, want %d (stderr=%q)", code, ExitOK, stderr)
	}
	// 超過レコードは出力されないため records=1、last_id は出力した最後のレコードから。
	if want := "records=1 files=1 last_id=1\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if !strings.Contains(stderr, "--max-record-bytes") || !strings.Contains(stderr, "スキップ") {
		t.Errorf("要件 6.6 の警告が標準エラー出力へ届いていません。stderr = %q", stderr)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("出力ファイルを読めません: %v", err)
	}
	if want := "{\"id\":1}\n"; string(got) != want {
		t.Errorf("出力ファイル = %q, want %q", got, want)
	}
}

// --- 要件 7.4: panic を回復して終了コード 9 に変換する ---

// 変換の途中(警告の書き出し)で panic した場合、出力ファイルを開始前状態へ戻したうえで
// 終了コード 9 を返し、panic の概要を標準エラー出力へ書く。
func TestRunRecoversPanicDuringConversion(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "in.json", `[{"id":1,"pad":"0123456789"}]`)
	out := filepath.Join(dir, "out.ndjson")

	var stdout bytes.Buffer
	stderr := &panicOnceWriter{}

	code := run([]string{
		"--in", in, "--out", out, "--max-record-bytes", "8", "--skip-oversize",
	}, &stdout, stderr)

	if code != ExitInternal {
		t.Fatalf("終了コード = %d, want %d", code, ExitInternal)
	}
	if !stderr.panicked {
		t.Fatal("テストが意図した panic 経路に到達していません")
	}
	if stdout.String() != "" {
		t.Errorf("異常終了では標準出力へ何も書かない(要件 8.1)。stdout = %q", stdout.String())
	}
	if msg := stderr.buf.String(); !strings.Contains(msg, "テスト由来の意図的な panic") {
		t.Errorf("panic の概要が標準エラー出力へ書かれていません。stderr = %q", msg)
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Errorf("panic からの回復でも出力パスにファイルを残してはいけません(要件 4.9): err = %v", err)
	}
	assertNoLeftovers(t, dir, out)
}

// 結果行の書き出しで panic した場合も回復して終了コード 9 を返す。
func TestRunRecoversPanicOnStdout(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "in.json", `[{"id":1}]`)
	out := filepath.Join(dir, "out.ndjson")

	stdout := &panicOnceWriter{}
	var stderr bytes.Buffer

	code := run([]string{"--in", in, "--out", out}, stdout, &stderr)

	if code != ExitInternal {
		t.Fatalf("終了コード = %d, want %d", code, ExitInternal)
	}
	if !strings.Contains(stderr.String(), "テスト由来の意図的な panic") {
		t.Errorf("panic の概要が標準エラー出力へ書かれていません。stderr = %q", stderr.String())
	}
}

// --- formatResultLine の単体検証(要件 5.2、5.6) ---

func TestFormatResultLine(t *testing.T) {
	cursor := func(s string) *string { return &s }

	tests := []struct {
		name    string
		result  *ConvertResult
		cursor  *string
		want    string
		wantErr bool
	}{
		{
			name:   "カーソルなし",
			result: &ConvertResult{Records: 3, Files: 1},
			want:   "records=3 files=1\n",
		},
		{
			name:   "カーソルあり",
			result: &ConvertResult{Records: 3, Files: 2},
			cursor: cursor("abc123"),
			want:   "records=3 files=2 last_id=abc123\n",
		},
		{
			name:   "0 件",
			result: &ConvertResult{Records: 0, Files: 1},
			want:   "records=0 files=1\n",
		},
		{
			name:    "非 ASCII のカーソル値は拒否する",
			result:  &ConvertResult{Records: 1, Files: 1},
			cursor:  cursor("あ"),
			wantErr: true,
		},
		{
			name:    "空白を含むカーソル値は拒否する",
			result:  &ConvertResult{Records: 1, Files: 1},
			cursor:  cursor("a b"),
			wantErr: true,
		},
		{
			name:    "制御文字を含むカーソル値は拒否する",
			result:  &ConvertResult{Records: 1, Files: 1},
			cursor:  cursor("a\nb"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := formatResultLine(tt.result, tt.cursor)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("エラーを期待しましたが got = %q", got)
				}
				if code := exitCodeOf(t, err); code != ExitLocation {
					t.Errorf("終了コード = %d, want %d (err=%v)", code, ExitLocation, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("予期しないエラー: %v", err)
			}
			if got != tt.want {
				t.Errorf("formatResultLine = %q, want %q", got, tt.want)
			}
		})
	}
}

// validateResultLine は組み立てた行全体に対する最後の関門(要件 5.6 / FR-31)。
// records / files が 10 進整数である以上 formatResultLine 経由では発火しないため、
// 規則そのものをここで固定する。
func TestValidateResultLine(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		wantErr bool
	}{
		{name: "区切りの空白は許す", line: "records=1 files=1 last_id=a"},
		{name: "記号を含む ASCII は許す", line: "records=1 files=1 last_id=a-b_c.d~e"},
		{name: "空の行も許す", line: ""},
		{name: "非 ASCII は拒否する", line: "records=1 files=1 last_id=あ", wantErr: true},
		{name: "制御文字は拒否する", line: "records=1 files=1\tlast_id=a", wantErr: true},
		{name: "改行は拒否する", line: "records=1\nfiles=1", wantErr: true},
		{name: "DEL は拒否する", line: "records=1 files=1 last_id=\x7f", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateResultLine(tt.line)
			if tt.wantErr {
				if err == nil {
					t.Fatal("エラーを期待しましたが nil でした")
				}
				if code := exitCodeOf(t, err); code != ExitLocation {
					t.Errorf("終了コード = %d, want %d", code, ExitLocation)
				}
				return
			}
			if err != nil {
				t.Errorf("予期しないエラー: %v", err)
			}
		})
	}
}

// 結果行は常に LF で終端する(design.md「Data Models / 結果行」)。
// --newline crlf は NDJSON ファイルの改行であり、結果行には影響しない。
func TestRunResultLineIsLFTerminatedRegardlessOfNewlineOption(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "in.json", `[{"id":1}]`)
	out := filepath.Join(dir, "out.ndjson")

	code, stdout, stderr := runCLI(t, "--in", in, "--out", out, "--newline", "crlf")

	if code != ExitOK {
		t.Fatalf("終了コード = %d, want %d (stderr=%q)", code, ExitOK, stderr)
	}
	if want := "records=1 files=1\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("出力ファイルを読めません: %v", err)
	}
	if want := "{\"id\":1}\r\n"; string(got) != want {
		t.Errorf("出力ファイル = %q, want %q", got, want)
	}
}
