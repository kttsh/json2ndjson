package main

import (
	"bytes"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// main_test.go は実行フロー(run)を、引数から終了コード・標準出力・標準エラー出力・
// 出力ファイルの状態まで一気通貫で検証する
// (design.md「Testing Strategy / Integration Tests 5. E2E(run 関数)」)。
//
// 収録している観点は 2 つの軸に分かれる。
//   - タスク 5.1(前半): 結線の観点。原因ごとに 1 ケースを置き、フローの各段
//     (番兵の表示要求、警告の宛先、defer による復元、panic 回復、確定の順序)を固定する
//   - タスク 5.2(後半、「タスク 5.2」の見出し以降): 終了コードの観点。6.8 節の
//     全コード(0〜5, 9)を 1 つの表で再現し、あわせて運用制約(空白・日本語パス、
//     冪等性、ネットワーク非依存)を検証する
//
// 同じ終了コードが両方に現れることがあるが、前者は「どの原因がその経路を通るか」、
// 後者は「体系のすべてのコードが再現できるか」を固定しており、軸が異なる。

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

// =============================================================================
// タスク 5.2: 終了コードマトリクスと運用制約
// =============================================================================

// TestExitCodeMatrix は要件 7.4(6.8 節)の終了コード体系のすべての値を
// 実際の実行で再現する。呼び出し元(DataSpider スクリプト)はこの値だけで
// 失敗理由を分岐するため、体系のどのコードも「到達できること」自体が契約である。
//
// 各ケースは終了コードだけでなく、標準出力と標準エラー出力の規律
// (要件 8.1 / 8.2)も同時に固定する。異常系で標準出力に何かが出れば、
// 呼び出し元の結果行の解析が壊れるためである。
func TestExitCodeMatrix(t *testing.T) {
	tests := []struct {
		name string
		// setup は入力を用意し、run へ渡す引数を返す。
		setup func(t *testing.T, dir string) []string
		// panicStderr が true のとき標準エラー出力を panicOnceWriter に差し替え、
		// 予期しない内部エラー(終了コード 9)を再現する。--skip-oversize の
		// 警告の書き出しで panic させることで、変換の途中から回復する経路を通す。
		panicStderr bool
		wantCode    int
		// wantStdout は標準出力の期待値。空文字列は「1 バイトも書かないこと」。
		wantStdout string
		// wantSilentStderr が true のとき標準エラー出力が空であることを要求する。
		// false のときは「空でないこと」を要求する。
		wantSilentStderr bool
	}{
		{
			name: "0 正常終了",
			setup: func(t *testing.T, dir string) []string {
				in := writeFile(t, dir, "in.json", `[{"id":1},{"id":2}]`)
				return []string{"--in", in, "--out", filepath.Join(dir, "out.ndjson")}
			},
			wantCode:         ExitOK,
			wantStdout:       "records=2 files=1\n",
			wantSilentStderr: true,
		},
		{
			name: "1 引数不正",
			setup: func(t *testing.T, dir string) []string {
				in := writeFile(t, dir, "in.json", `[]`)
				// --append と --overwrite は排他(要件 7.3 / FR-37)。
				return []string{"--in", in, "--out", filepath.Join(dir, "out.ndjson"), "--append", "--overwrite"}
			},
			wantCode: ExitUsage,
		},
		{
			name: "2 入力ファイル不可",
			setup: func(t *testing.T, dir string) []string {
				return []string{"--in", filepath.Join(dir, "存在しない.json"), "--out", filepath.Join(dir, "out.ndjson")}
			},
			wantCode: ExitInput,
		},
		{
			name: "3 JSON 構文エラー",
			setup: func(t *testing.T, dir string) []string {
				in := writeFile(t, dir, "in.json", `[{"id":1},{`)
				return []string{"--in", in, "--out", filepath.Join(dir, "out.ndjson")}
			},
			wantCode: ExitSyntax,
		},
		{
			name: "4 出力書き込みエラー",
			setup: func(t *testing.T, dir string) []string {
				in := writeFile(t, dir, "in.json", `[{"id":1}]`)
				// 既存の出力ファイルがあり --overwrite がない(要件 4.7 / FR-26)。
				out := writeFile(t, dir, "out.ndjson", "既存の内容\n")
				return []string{"--in", in, "--out", out}
			},
			wantCode: ExitOutput,
		},
		{
			name: "5 位置不正",
			setup: func(t *testing.T, dir string) []string {
				in := writeFile(t, dir, "in.json", `{"data":"配列でもオブジェクトでもない"}`)
				return []string{"--in", in, "--out", filepath.Join(dir, "out.ndjson"), "--path", "/data"}
			},
			wantCode: ExitLocation,
		},
		{
			name: "9 予期しない内部エラー",
			setup: func(t *testing.T, dir string) []string {
				in := writeFile(t, dir, "in.json", `[{"id":1,"pad":"0123456789"}]`)
				return []string{
					"--in", in, "--out", filepath.Join(dir, "out.ndjson"),
					"--max-record-bytes", "8", "--skip-oversize",
				}
			},
			panicStderr: true,
			wantCode:    ExitInternal,
		},
	}

	// 体系のすべてのコードが表に現れていることを、表そのものから確かめる。
	// ケースを消したり書き換えたりすると、マトリクスとしての網羅性が静かに失われるため。
	covered := map[int]bool{}
	for _, tt := range tests {
		covered[tt.wantCode] = true
	}
	for _, code := range []int{ExitOK, ExitUsage, ExitInput, ExitSyntax, ExitOutput, ExitLocation, ExitInternal} {
		if !covered[code] {
			t.Errorf("終了コード %d を再現するケースが表にありません(要件 7.4)", code)
		}
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			args := tt.setup(t, dir)

			var stdout bytes.Buffer
			var plainStderr bytes.Buffer
			var stderr io.Writer = &plainStderr
			var panicked *panicOnceWriter
			if tt.panicStderr {
				panicked = &panicOnceWriter{}
				stderr = panicked
			}

			code := run(args, &stdout, stderr)

			stderrText := plainStderr.String()
			if panicked != nil {
				if !panicked.panicked {
					t.Fatal("意図した panic 経路に到達していません")
				}
				stderrText = panicked.buf.String()
			}

			if code != tt.wantCode {
				t.Errorf("終了コード = %d, want %d (stderr=%q)", code, tt.wantCode, stderrText)
			}
			got := stdout.String()
			if got != tt.wantStdout {
				t.Errorf("stdout = %q, want %q", got, tt.wantStdout)
			}
			// 標準出力は終了コードによらず ASCII の範囲に収める(要件 5.6 / 8.3)。
			for i := range len(got) {
				if b := got[i]; b > 0x7e {
					t.Errorf("stdout の %d バイト目が ASCII の範囲外(0x%02x)です: %q", i+1, b, got)
					break
				}
			}
			if tt.wantSilentStderr {
				if stderrText != "" {
					t.Errorf("正常終了では stderr へ何も書かない(要件 8.2)。stderr = %q", stderrText)
				}
				return
			}
			if stderrText == "" {
				t.Error("異常終了では stderr へ人間向けメッセージを書く(要件 8.2)")
			}
			// 標準エラー出力は UTF-8 の人間向けメッセージ(要件 8.3 / FR-42)。
			// Go のソース文字列は元より UTF-8 なのでほぼ自明だが、日本語 Windows 環境向けの
			// ツールでは「コンソールに合わせて Shift_JIS へ変換する」改変が現実的にありうる。
			// この検査はその退行を捕まえるために置いている。
			if !utf8.ValidString(stderrText) {
				t.Errorf("stderr が UTF-8 として不正です: %q", stderrText)
			}
		})
	}
}

// TestRunHandlesSpaceAndJapanesePaths は空白と日本語を含む絶対パスでの入出力を検証する
// (要件 4.1 / FR-20)。DataSpider の運用では出力先が日本語のディレクトリになりうる。
func TestRunHandlesSpaceAndJapanesePaths(t *testing.T) {
	root := t.TempDir()
	inDir := filepath.Join(root, "入力 ディレクトリ")
	outDir := filepath.Join(root, "出力 用 フォルダ")
	for _, d := range []string{inDir, outDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("テスト用ディレクトリ %q を作成できません: %v", d, err)
		}
	}

	in := writeFile(t, inDir, "テスト 入力 データ.json", `{"データ":[{"id":"あ1"},{"id":"あ2"}]}`)
	out := filepath.Join(outDir, "結果 出力.ndjson")

	// 要件 4.1 が定めるのは絶対パスでの受け付け。前提が崩れていないことを明示する。
	if !filepath.IsAbs(in) || !filepath.IsAbs(out) {
		t.Fatalf("テストの前提が壊れています(絶対パスではない): in=%q out=%q", in, out)
	}

	// --cursor-key の値は ASCII に収める必要がある(要件 5.6)ため、
	// 結果行に載せない形で日本語の値を通す。
	code, stdout, stderr := runCLI(t, "--in", in, "--out", out, "--path", "/データ")
	if code != ExitOK {
		t.Fatalf("終了コード = %d, want %d (stderr=%q)", code, ExitOK, stderr)
	}
	if want := "records=2 files=1\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("出力ファイルを読めません: %v", err)
	}
	// 日本語のキーと値は UTF-8 のまま出力する(要件 3.4 / 3.9)。
	if want := "{\"id\":\"あ1\"}\n{\"id\":\"あ2\"}\n"; string(got) != want {
		t.Errorf("出力ファイル = %q, want %q", got, want)
	}

	// 追記モードでも同じパスを扱えること(要件 4.3)。
	code, stdout, stderr = runCLI(t, "--in", in, "--out", out, "--path", "/データ", "--append")
	if code != ExitOK {
		t.Fatalf("追記の終了コード = %d, want %d (stderr=%q)", code, ExitOK, stderr)
	}
	if want := "records=2 files=1\n"; stdout != want {
		t.Errorf("追記の stdout = %q, want %q", stdout, want)
	}
	got, err = os.ReadFile(out)
	if err != nil {
		t.Fatalf("出力ファイルを読めません: %v", err)
	}
	if want := strings.Repeat("{\"id\":\"あ1\"}\n{\"id\":\"あ2\"}\n", 2); string(got) != want {
		t.Errorf("追記後の出力ファイル = %q, want %q", got, want)
	}
	assertNoLeftovers(t, outDir, out)
}

// TestRunIsIdempotent は同じ入力に対する再実行が同じ出力を生むことを検証する
// (要件 10.1 / NFR-06)。呼び出し元は失敗時にそのまま再実行するため、
// 実行ごとに結果が変わると復旧手順が成立しない。
//
// 対象は新規作成モード。追記モードは「既存の末尾に足す」ことが仕様であり
// (要件 4.3)、再実行で内容が増えるのは冪等性の違反ではない。
//
// 整形オプションを一通り有効にして実行する。再エンコード経路(キー置換・固定値追加)や
// 重複排除の集合を通るため、マップの反復順のような非決定性が紛れ込めばここで露見する。
func TestRunIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "in.json", `{"data":[
		{"user-id":"u1","tag-name":"a","nest":{"inner-key":[1,2,{"deep-key":true}]}},
		{"user-id":"u2","tag-name":"b","nest":{"inner-key":[]}},
		{"user-id":"u1","tag-name":"重複により落ちる"},
		{"user-id":"u3","pad":"上限を超えるのでスキップされる長い値です上限を超えるのでスキップされる長い値です"}
	]}`)
	out := filepath.Join(dir, "out.ndjson")

	args := []string{
		"--in", in, "--out", out, "--overwrite",
		"--path", "/data",
		// カーソル値も重複排除キーも整形「後」のレコードから解決されるため、
		// --key-hyphen-to-underscore を有効にした場合は置換後のキー名で指定する
		// (整形の適用順序は「キー名置換 → 固定値追加 → サイズ上限 → 重複排除」)。
		"--cursor-key", "/user_id",
		"--dedupe-key", "/user_id",
		"--key-hyphen-to-underscore",
		"--add-field", "source=api",
		"--add-field", "batch=001",
		"--newline", "crlf",
		// 整形後のサイズは 1 件目 105 / 2 件目 84 / 4 件目 174 バイト。
		// 110 は前 2 件を通し 4 件目だけを落とす値で、両側に余裕がある。
		"--max-record-bytes", "110",
		"--skip-oversize",
	}

	type observation struct {
		code    int
		stdout  string
		stderr  string
		content string
	}
	var runs []observation
	for range 3 {
		code, stdout, stderr := runCLI(t, args...)
		content, err := os.ReadFile(out)
		if err != nil {
			t.Fatalf("出力ファイルを読めません: %v", err)
		}
		runs = append(runs, observation{code, stdout, stderr, string(content)})
	}

	first := runs[0]
	if first.code != ExitOK {
		t.Fatalf("終了コード = %d, want %d (stderr=%q)", first.code, ExitOK, first.stderr)
	}
	// 重複 1 件と上限超過 1 件が落ちるため、出力は 2 件。
	if want := "records=2 files=1 last_id=u2\n"; first.stdout != want {
		t.Errorf("stdout = %q, want %q", first.stdout, want)
	}
	// 整形が実際に効いていることを確かめる。効いていない出力を 3 回比べても
	// 冪等性の証明にはならないため、内容そのものをここで固定する。
	wantContent := `{"user_id":"u1","tag_name":"a","nest":{"inner_key":[1,2,{"deep_key":true}]},"source":"api","batch":"001"}` + "\r\n" +
		`{"user_id":"u2","tag_name":"b","nest":{"inner_key":[]},"source":"api","batch":"001"}` + "\r\n"
	if first.content != wantContent {
		t.Errorf("出力ファイル = %q\nwant %q", first.content, wantContent)
	}
	if !strings.Contains(first.stderr, "--max-record-bytes") {
		t.Errorf("上限超過のスキップ警告が出ていません。stderr = %q", first.stderr)
	}

	for i, r := range runs[1:] {
		n := i + 2
		if r.code != first.code {
			t.Errorf("%d 回目の終了コード = %d, want %d(1 回目と同じであること)", n, r.code, first.code)
		}
		if r.stdout != first.stdout {
			t.Errorf("%d 回目の stdout = %q, want %q(1 回目と同じであること)", n, r.stdout, first.stdout)
		}
		if r.stderr != first.stderr {
			t.Errorf("%d 回目の stderr = %q, want %q(1 回目と同じであること)", n, r.stderr, first.stderr)
		}
		if r.content != first.content {
			t.Errorf("%d 回目の出力ファイルが 1 回目と異なります(要件 10.1)\n got %q\nwant %q", n, r.content, first.content)
		}
	}
	assertNoLeftovers(t, dir, out)
}

// TestRunRetryAfterFailureMatchesCleanRun は「失敗 → 復元 → 再実行」の結果が
// 一度も失敗しなかった実行の結果と一致することを検証する(要件 10.1 / NFR-06)。
//
// 要件 10 の目的は「障害時のリトライと原因調査を安心して行える」ことであり、
// 冪等性が意味を持つのはまさにこの経路である。失敗した実行がロールバックしても
// 副作用(部分的な行、残ったジャーナル、ずれた開始前サイズ)を残していれば、
// 再実行の結果は clean run と一致しない。
func TestRunRetryAfterFailureMatchesCleanRun(t *testing.T) {
	const seed = "{\"id\":0}\n"
	good := `[{"id":1},{"id":2}]`
	broken := `[{"id":`

	// 対照群: 一度も失敗せず、良い入力だけを追記する。
	controlDir := t.TempDir()
	controlIn := writeFile(t, controlDir, "a.json", good)
	controlOut := writeFile(t, controlDir, "out.ndjson", seed)
	code, wantStdout, stderr := runCLI(t, "--in", controlIn, "--out", controlOut, "--append", "--cursor-key", "/id")
	if code != ExitOK {
		t.Fatalf("対照群の終了コード = %d, want %d (stderr=%q)", code, ExitOK, stderr)
	}
	wantContent, err := os.ReadFile(controlOut)
	if err != nil {
		t.Fatalf("対照群の出力を読めません: %v", err)
	}

	// 試験群: まず壊れた入力を含めて失敗させ、復元後に良い入力だけで再実行する。
	dir := t.TempDir()
	in := writeFile(t, dir, "a.json", good)
	bad := writeFile(t, dir, "b.json", broken)
	out := writeFile(t, dir, "out.ndjson", seed)

	code, stdout, stderr := runCLI(t, "--in", in, "--in", bad, "--out", out, "--append", "--cursor-key", "/id")
	if code != ExitSyntax {
		t.Fatalf("1 回目(失敗させる実行)の終了コード = %d, want %d (stderr=%q)", code, ExitSyntax, stderr)
	}
	if stdout != "" {
		t.Errorf("失敗した実行が標準出力へ書いています: %q", stdout)
	}
	// 失敗の直後は追記開始前の状態に戻っていること(要件 4.9)。
	if got, err := os.ReadFile(out); err != nil {
		t.Fatalf("出力ファイルを読めません: %v", err)
	} else if string(got) != seed {
		t.Fatalf("失敗後の出力 = %q, want %q", got, seed)
	}
	assertNoLeftovers(t, dir, out)

	// リトライ。壊れた入力を取り除いた以外は対照群と同じ実行になる。
	code, stdout, stderr = runCLI(t, "--in", in, "--out", out, "--append", "--cursor-key", "/id")
	if code != ExitOK {
		t.Fatalf("リトライの終了コード = %d, want %d (stderr=%q)", code, ExitOK, stderr)
	}
	if stdout != wantStdout {
		t.Errorf("リトライの結果行 = %q, want %q(一度も失敗しなかった実行と同じであること)", stdout, wantStdout)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("出力ファイルを読めません: %v", err)
	}
	if string(got) != string(wantContent) {
		t.Errorf("リトライ後の出力 = %q\nwant %q(一度も失敗しなかった実行と同じであること。要件 10.1)", got, wantContent)
	}
	assertNoLeftovers(t, dir, out)
}

// TestNoNetworkPackagesInSource はパッケージ自身のソースがネットワーク系の
// パッケージを import していないことを検証する(要件 11.3 / NFR-12)。
//
// go ツールチェーンを必要としないため常に実行される。ビルドタグで分離された
// ファイル(lock_windows.go など)も含めて全ソースを走査する。
func TestNoNetworkPackagesInSource(t *testing.T) {
	// ファイルを 1 つずつ解析する(go/parser の ParseDir はビルドタグを考慮しないと
	// して非推奨。ここでは逆にタグを無視して全ファイルを見たいので、意図を
	// そのまま書き下すほうが正確である)。
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("ソースファイルを列挙できません: %v", err)
	}

	fset := token.NewFileSet()
	checked := 0
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("%s を解析できません: %v", name, err)
		}
		checked++
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s: import パスを解釈できません: %v", name, err)
			}
			if isNetworkPackage(path) {
				t.Errorf("%s がネットワーク系パッケージ %q を import しています(要件 11.3)", name, path)
			}
		}
	}
	if checked == 0 {
		t.Fatal("走査したファイルが 0 件です(テストの前提が壊れています)")
	}
	t.Logf("走査したソースファイル数: %d", checked)
}

// TestNoNetworkPackagesInDependencies は推移的な依存も含めてネットワーク系の
// パッケージに到達しないことを検証する(要件 11.3 / NFR-12)。
//
// 自身の import だけを見ても、依存先がネットワークを引き込む可能性は排除できない。
// go list が唯一の正確な情報源のため、ツールチェーンを呼び出す。
func TestNoNetworkPackagesInDependencies(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		// 事前ビルド済みのテストバイナリを単体で走らせた場合にのみ起こる。
		// ソース側の検査(TestNoNetworkPackagesInSource)は常に実行される。
		t.Skipf("go ツールチェーンが見つからないため推移的依存の検査を省略します: %v", err)
	}

	cmd := exec.CommandContext(t.Context(), goBin, "list", "-deps", ".")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps に失敗しました: %v\nstderr: %s", err, stderr.String())
	}

	deps := strings.Fields(string(out))
	if len(deps) == 0 {
		t.Fatal("依存パッケージが 0 件です(テストの前提が壊れています)")
	}
	for _, dep := range deps {
		if isNetworkPackage(dep) {
			t.Errorf("推移的な依存にネットワーク系パッケージ %q が含まれます(要件 11.3)", dep)
		}
	}
	t.Logf("検査した依存パッケージ数: %d", len(deps))
}

// isNetworkPackage は import パスがネットワーク系かどうかを返す。
//
// design.md「Security Considerations」の「net 系パッケージを import しない」に従い、
// net ツリー全体を対象とする。net/url のようにそれ自体は通信しないものも含めるのは、
// ファイルからファイルへ変換する本ツールに URL を扱う理由がなく、
// 例外を設けると判定が主観的になるため。
func isNetworkPackage(importPath string) bool {
	for _, banned := range []string{"net", "crypto/tls", "golang.org/x/net"} {
		if importPath == banned || strings.HasPrefix(importPath, banned+"/") {
			return true
		}
	}
	return false
}
