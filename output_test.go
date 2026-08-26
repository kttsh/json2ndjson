package main

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// OutputManager が convert の書き込み境界(RecordSink)を満たすことをコンパイル時に固定する。
// convert は OutputManager を直接知らないため、この宣言がなければ署名の食い違いは
// タスク 5.1 の結線まで検出されない。
var _ RecordSink = (*OutputManager)(nil)

// utf8BOM は出力に決して現れてはならないバイト列(要件 3.4 / FR-13)。
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// newOutputConfig は新規作成モード(--append なし)の最小構成を返す。
// 既定は cli の既定値と同じ「LF・最終行改行あり」(要件 3.3 / 3.5)。
func newOutputConfig(out string) *Config {
	return &Config{Out: out, Newline: "\n", TrailingNewline: true}
}

// mustOpenOutput は openOutput の成功を前提に OutputManager を返す。
func mustOpenOutput(t *testing.T, cfg *Config) *OutputManager {
	t.Helper()
	om, err := openOutput(cfg)
	if err != nil {
		t.Fatalf("openOutput(%q) が失敗した: %v", cfg.Out, err)
	}
	return om
}

// writeRecords は与えられたレコードを順に書き込む。
func writeRecords(t *testing.T, om *OutputManager, records ...string) {
	t.Helper()
	for _, rec := range records {
		if err := om.WriteRecord(jsontext.Value(rec)); err != nil {
			t.Fatalf("WriteRecord(%q) が失敗した: %v", rec, err)
		}
	}
}

// readOutput はファイル全体を読み、読めなければテストを失敗させる。
func readOutput(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("出力ファイル %q を読めない: %v", path, err)
	}
	return b
}

// assertNotExist は path が存在しないことを検証する。
func assertNotExist(t *testing.T, path, label string) {
	t.Helper()
	if _, err := os.Lstat(path); err == nil {
		t.Errorf("%s %q が存在してはならないのに存在する", label, path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s %q の存在確認に失敗した: %v", label, path, err)
	}
}

// 終了コードの取り出しには transform_test.go の exitCodeOf を用いる
// (ExitError でなければ失敗する同じ規律。main は errors.As でしか分岐できないため、
// 素の error を返す実装は終了コード 4 を呼び出し元へ伝えられない)。

// tempPathOf はテストが期待する一時ファイルのパス(design.md「output」の `<出力名>.tmp`)。
func tempPathOf(out string) string {
	return out + ".tmp"
}

// TestFinalizeNewFileWritesExactBytes は、新規作成モードの出力バイト列を
// 改行コード(要件 3.3)・最終行改行(要件 3.5)・レコード件数(要件 3.10)の
// 全組み合わせについてバイト単位で固定する。
//
// 「区切りとしての改行」と「最終行の改行」の境界を取り違える実装
// (0 件なのに改行 1 個を書く、最終行改行なし指定でも末尾に改行を書く、など)は
// ここで必ず落ちる。
func TestFinalizeNewFileWritesExactBytes(t *testing.T) {
	tests := []struct {
		name     string
		newline  string
		trailing bool
		records  []string
		want     string
	}{
		{"LF・最終行改行あり・0 件", "\n", true, nil, ""},
		{"LF・最終行改行なし・0 件", "\n", false, nil, ""},
		{"CRLF・最終行改行あり・0 件", "\r\n", true, nil, ""},
		{"LF・最終行改行あり・1 件", "\n", true, []string{`{"a":1}`}, "{\"a\":1}\n"},
		{"LF・最終行改行なし・1 件", "\n", false, []string{`{"a":1}`}, `{"a":1}`},
		{"LF・最終行改行あり・3 件", "\n", true,
			[]string{`{"a":1}`, `{"a":2}`, `{"a":3}`},
			"{\"a\":1}\n{\"a\":2}\n{\"a\":3}\n"},
		{"LF・最終行改行なし・3 件", "\n", false,
			[]string{`{"a":1}`, `{"a":2}`, `{"a":3}`},
			"{\"a\":1}\n{\"a\":2}\n{\"a\":3}"},
		{"CRLF・最終行改行あり・1 件", "\r\n", true, []string{`{"a":1}`}, "{\"a\":1}\r\n"},
		{"CRLF・最終行改行なし・1 件", "\r\n", false, []string{`{"a":1}`}, `{"a":1}`},
		{"CRLF・最終行改行あり・3 件", "\r\n", true,
			[]string{`{"a":1}`, `{"a":2}`, `{"a":3}`},
			"{\"a\":1}\r\n{\"a\":2}\r\n{\"a\":3}\r\n"},
		{"CRLF・最終行改行なし・3 件", "\r\n", false,
			[]string{`{"a":1}`, `{"a":2}`, `{"a":3}`},
			"{\"a\":1}\r\n{\"a\":2}\r\n{\"a\":3}"},
		{"日本語・絵文字・エスケープを含むレコードを素通しする", "\n", true,
			[]string{`{"名前":"山田 太郎","絵文字":"😀","改行":"a\nb"}`},
			"{\"名前\":\"山田 太郎\",\"絵文字\":\"😀\",\"改行\":\"a\\nb\"}\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			out := filepath.Join(dir, "out.ndjson")
			cfg := newOutputConfig(out)
			cfg.Newline = tt.newline
			cfg.TrailingNewline = tt.trailing

			om := mustOpenOutput(t, cfg)
			writeRecords(t, om, tt.records...)
			if err := om.Finalize(); err != nil {
				t.Fatalf("Finalize が失敗した: %v", err)
			}

			got := readOutput(t, out)
			if string(got) != tt.want {
				t.Errorf("出力バイト列が期待と異なる\n got=%q\nwant=%q", got, tt.want)
			}
			if bytes.HasPrefix(got, utf8BOM) {
				t.Error("出力の先頭に BOM が書かれている(要件 3.4 は BOM なし UTF-8)")
			}
			// 確定後に一時ファイルが残っていると、次回実行が stale 判定を通ることになる。
			assertNotExist(t, tempPathOf(out), "一時ファイル")
		})
	}
}

// TestFinalizeWithZeroRecordsCreatesEmptyFile は、レコード 0 件でも 0 バイトの
// 出力ファイルが作られることを検証する(要件 3.10 / FR-19)。
//
// タスク 2.4 は「空配列 → レコード 0 件」の変換経路までしか検証していない
// (tasks.md「タスク 2.4 が常設したテスト資産」の委譲事項)。ファイル層で
// 0 バイトのファイルが実際に生成されるかは、この 1 本だけが押さえている。
func TestFinalizeWithZeroRecordsCreatesEmptyFile(t *testing.T) {
	for _, trailing := range []bool{true, false} {
		name := "最終行改行なし"
		if trailing {
			name = "最終行改行あり"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			out := filepath.Join(dir, "out.ndjson")
			cfg := newOutputConfig(out)
			cfg.TrailingNewline = trailing

			om := mustOpenOutput(t, cfg)
			if err := om.Finalize(); err != nil {
				t.Fatalf("Finalize が失敗した: %v", err)
			}

			info, err := os.Stat(out)
			if err != nil {
				t.Fatalf("0 件でも出力ファイルは作成されねばならない: %v", err)
			}
			if info.Size() != 0 {
				got := readOutput(t, out)
				t.Errorf("0 件の出力が %d バイト(%q)。0 バイトでなければならない", info.Size(), got)
			}
			if !info.Mode().IsRegular() {
				t.Errorf("出力が通常ファイルではない: %v", info.Mode())
			}
		})
	}
}

// TestOpenOutputRefusesExistingFileWithoutOverwrite は、--overwrite なしで
// 既存の出力ファイルがある場合に、書き込みを一切開始せず終了コード 4 で
// 停止することを検証する(要件 4.7 / FR-26)。
func TestOpenOutputRefusesExistingFileWithoutOverwrite(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	original := []byte("{\"既存\":\"この内容は保たれねばならない\"}\n")
	if err := os.WriteFile(out, original, 0o644); err != nil {
		t.Fatalf("既存ファイルを準備できない: %v", err)
	}

	om, err := openOutput(newOutputConfig(out))
	if om != nil {
		t.Error("既存ファイルがあるのに OutputManager が返った(書き込み開始前に停止しなければならない)")
	}
	if code := exitCodeOf(t, err); code != ExitOutput {
		t.Errorf("終了コードが %d。既存ファイル+--overwrite なしは %d でなければならない", code, ExitOutput)
	}
	if !strings.Contains(err.Error(), out) {
		t.Errorf("エラーメッセージに出力パスが含まれない: %v", err)
	}

	// 既存ファイルは 1 バイトも変わってはならない(切り詰め・上書きの禁止)。
	if got := readOutput(t, out); !bytes.Equal(got, original) {
		t.Errorf("既存ファイルが改変された\n got=%q\nwant=%q", got, original)
	}
	// 一時ファイルも作ってはならない(書き込み開始前に停止する)。
	assertNotExist(t, tempPathOf(out), "一時ファイル")
}

// TestFinalizeReplacesExistingFileWithOverwrite は、--overwrite 指定時に
// 既存ファイルが新しい内容へ完全に置き換わることを検証する(要件 4.2 / FR-21)。
func TestFinalizeReplacesExistingFileWithOverwrite(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	// 新しい内容より長い既存内容を置く。rename ではなく上書き書き込みで
	// 実装すると、末尾に古い内容が残ってこのテストが落ちる。
	if err := os.WriteFile(out, []byte(strings.Repeat("{\"古い\":\"内容\"}\n", 50)), 0o644); err != nil {
		t.Fatalf("既存ファイルを準備できない: %v", err)
	}

	cfg := newOutputConfig(out)
	cfg.Overwrite = true
	om := mustOpenOutput(t, cfg)
	writeRecords(t, om, `{"新しい":"内容"}`)
	if err := om.Finalize(); err != nil {
		t.Fatalf("Finalize が失敗した: %v", err)
	}

	const want = "{\"新しい\":\"内容\"}\n"
	if got := readOutput(t, out); string(got) != want {
		t.Errorf("上書き後の内容が期待と異なる\n got=%q\nwant=%q", got, want)
	}
	assertNotExist(t, tempPathOf(out), "一時ファイル")
}

// TestOpenOutputCreatesTempInSameDirectoryAndDefersOutputPath は、
//   - 一時ファイルが出力ファイルと同一ディレクトリに作られること(要件 11.4 / NFR-13)
//   - 確定するまで出力パスにファイルが現れないこと(要件 4.2 / FR-21)
//
// を、書き込み中のディレクトリ内容を列挙して検証する。
func TestOpenOutputCreatesTempInSameDirectoryAndDefersOutputPath(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	om := mustOpenOutput(t, newOutputConfig(out))
	writeRecords(t, om, `{"a":1}`, `{"a":2}`)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("出力ディレクトリを列挙できない: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != filepath.Base(tempPathOf(out)) {
		t.Errorf("書き込み中のディレクトリ内容が %v。%q ただ 1 つでなければならない",
			names, filepath.Base(tempPathOf(out)))
	}
	// 一時ファイルの置き場所が出力と別ボリュームだと rename が失敗しうる(NFR-13)。
	if got := filepath.Dir(tempPathOf(out)); got != dir {
		t.Errorf("一時ファイルのディレクトリが %q。出力と同じ %q でなければならない", got, dir)
	}
	// 確定前に出力パスが存在すると、失敗時に中途半端なファイルが残る。
	assertNotExist(t, out, "確定前の出力ファイル")

	if err := om.Finalize(); err != nil {
		t.Fatalf("Finalize が失敗した: %v", err)
	}
	if got, want := string(readOutput(t, out)), "{\"a\":1}\n{\"a\":2}\n"; got != want {
		t.Errorf("確定後の内容が期待と異なる\n got=%q\nwant=%q", got, want)
	}
}

// TestAbortAfterPartialWriteLeavesNothing は、書き込み途中で中断した場合に
// 出力パスにファイルが存在せず、一時ファイルも残らないことを検証する
// (要件 4.2 / FR-21、要件 4.9 / FR-39、タスク 3.2 の完了条件)。
func TestAbortAfterPartialWriteLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	om := mustOpenOutput(t, newOutputConfig(out))
	writeRecords(t, om, `{"a":1}`, `{"a":2}`)

	om.Abort()

	assertNotExist(t, out, "中断後の出力ファイル")
	assertNotExist(t, tempPathOf(out), "中断後の一時ファイル")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("出力ディレクトリを列挙できない: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("中断後にディレクトリへ残骸が残っている: %v", entries)
	}
}

// TestAbortWithOverwriteKeepsPreExistingFile は、--overwrite 指定で既存ファイルを
// 置き換える途中で中断した場合に、既存ファイルが元のまま残ることを検証する
// (要件 4.9 / FR-39 の「実行前と同じ状態」、要件 4.2 / FR-21 の「出力パスに触れない」)。
//
// design.md「output」の Postconditions は新規モードの中断後を「不在」と書いているが、
// それは出力が元から存在しなかった場合の話である。--overwrite で既存ファイルを
// 置き換える途中に中断したとき、中断処理が出力パスを削除してしまうと、実行前に
// あったデータを失う。中断は一時ファイルの削除だけで完結しなければならない。
func TestAbortWithOverwriteKeepsPreExistingFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	original := []byte("{\"実行前\":\"この内容は中断後も残らねばならない\"}\n")
	if err := os.WriteFile(out, original, 0o644); err != nil {
		t.Fatalf("既存ファイルを準備できない: %v", err)
	}

	cfg := newOutputConfig(out)
	cfg.Overwrite = true
	om := mustOpenOutput(t, cfg)
	writeRecords(t, om, `{"a":1}`)
	om.Abort()

	if got := readOutput(t, out); !bytes.Equal(got, original) {
		t.Errorf("中断で既存ファイルが失われた/変わった\n got=%q\nwant=%q", got, original)
	}
	assertNotExist(t, tempPathOf(out), "中断後の一時ファイル")
}

// TestWriteRecordReportsRealWriteFailure は、実際の write(2) の失敗を注入し、
//   - WriteRecord が終了コード 4 のエラーとして報告すること
//   - その後の Abort で出力パスにファイルが存在しないこと(タスク 3.2 の完了条件)
//
// を検証する。注入は「下位のファイルハンドルを閉じてから、bufio のバッファを
// 超える大きさのレコードを書く」ことで行う。バッファ超過により必ず実書き込みが
// 走るため、モックではなく OS が返す本物のエラーを経路に通せる。
func TestWriteRecordReportsRealWriteFailure(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	om := mustOpenOutput(t, newOutputConfig(out))
	writeRecords(t, om, `{"a":1}`)

	if err := om.file.Close(); err != nil {
		t.Fatalf("失敗注入のためのクローズができない: %v", err)
	}

	huge := jsontext.Value(`"` + strings.Repeat("x", 4*writeBufferSize) + `"`)
	err := om.WriteRecord(huge)
	if err == nil {
		t.Fatal("閉じたファイルへの書き込みが成功として報告された")
	}
	if code := exitCodeOf(t, err); code != ExitOutput {
		t.Errorf("書き込み失敗の終了コードが %d。%d でなければならない", code, ExitOutput)
	}
	if !strings.Contains(err.Error(), out) && !strings.Contains(err.Error(), tempPathOf(out)) {
		t.Errorf("エラーメッセージにパスが含まれない: %v", err)
	}

	om.Abort()
	assertNotExist(t, out, "書き込み失敗後の出力ファイル")
	assertNotExist(t, tempPathOf(out), "書き込み失敗後の一時ファイル")
}

// TestFinalizeFailureLeavesNoOutput は、確定処理(Flush→Sync→rename)の途中で
// 失敗した場合にも出力パスにファイルが現れず、一時ファイルも残らないことを
// 検証する(要件 4.2 / FR-21)。
//
// ハンドルを先に閉じることで確定手順を失敗させる。バッファに未書き出しの
// レコードがある場合は Flush で、レコード 0 件の場合は Flush を素通りして
// その後の段(Sync / Close)で失敗するため、確定手順のどの段で失敗しても
// 出力パスに触れないことを両方の経路で確かめられる。
// 失敗を検出せず rename まで進む実装、失敗時に一時ファイルを掃除しない実装は落ちる。
func TestFinalizeFailureLeavesNoOutput(t *testing.T) {
	tests := []struct {
		name    string
		records []string
	}{
		{"Flush で失敗する(未書き出しのレコードあり)", []string{`{"a":1}`}},
		{"Flush 通過後に失敗する(レコード 0 件)", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			out := filepath.Join(dir, "out.ndjson")
			om := mustOpenOutput(t, newOutputConfig(out))
			writeRecords(t, om, tt.records...)

			if err := om.file.Close(); err != nil {
				t.Fatalf("失敗注入のためのクローズができない: %v", err)
			}

			err := om.Finalize()
			if err == nil {
				t.Fatal("閉じたハンドルに対する Finalize が成功として報告された")
			}
			if code := exitCodeOf(t, err); code != ExitOutput {
				t.Errorf("確定失敗の終了コードが %d。%d でなければならない", code, ExitOutput)
			}
			assertNotExist(t, out, "確定失敗後の出力ファイル")
			assertNotExist(t, tempPathOf(out), "確定失敗後の一時ファイル")

			// 確定に失敗した後は中断済みとして扱う。main の defer が Abort を
			// 重ねても、二重に片付けが走ったり出力パスに触れたりしてはならない。
			om.Abort()
			assertNotExist(t, out, "確定失敗+中断後の出力ファイル")
		})
	}
}

// TestOpenOutputRemovesStaleTemp は、ロックされていない一時ファイルの残骸
// (前回の強制終了などで残ったもの)を stale とみなして削除し、処理を継続できる
// ことを検証する(design.md「output」の「既存なら stale 判定」)。
//
// 【この環境での限界】非 Windows の tryLock は常に成功する no-op のため
// (tasks.md「未配線の契約」)、残骸は常に stale と判定される。すなわち
// 「他プロセスが使用中の一時ファイルを誤って削除しない」ことは Linux では
// 証明できない。その分岐は TestOpenOutputRefusesLockedTemp が Windows でのみ検証する。
func TestOpenOutputRemovesStaleTemp(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	stale := []byte("{\"前回の残骸\":true}\n途中で切れた行")
	if err := os.WriteFile(tempPathOf(out), stale, 0o644); err != nil {
		t.Fatalf("残骸の一時ファイルを準備できない: %v", err)
	}

	om, err := openOutput(newOutputConfig(out))
	if err != nil {
		t.Fatalf("未ロックの残骸があると openOutput が失敗した: %v", err)
	}
	writeRecords(t, om, `{"a":1}`)
	if err := om.Finalize(); err != nil {
		t.Fatalf("Finalize が失敗した: %v", err)
	}

	got := readOutput(t, out)
	if string(got) != "{\"a\":1}\n" {
		t.Errorf("残骸の内容が出力へ混入した\n got=%q\nwant=%q", got, "{\"a\":1}\n")
	}
	if bytes.Contains(got, []byte("残骸")) {
		t.Error("stale な一時ファイルが切り詰められずに再利用されている")
	}
	assertNotExist(t, tempPathOf(out), "確定後の一時ファイル")
}

// TestOpenOutputRefusesLockedTemp は、他プロセスがロック中の一時ファイルが
// ある場合に終了コード 4 で停止することを検証する(要件 4.6 / FR-25)。
//
// 非 Windows では tryLock が no-op で常に成功するため、この分岐は原理的に
// 到達できない(tasks.md「未配線の契約」: タスク 3.2 は Linux 上で
// 「同時実行 → 終了コード 4」を検証済みと主張してはならない)。
func TestOpenOutputRefusesLockedTemp(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("非 Windows の tryLock は no-op のため、ロック済み一時ファイルを再現できない(要件 4.6 は Windows 受け入れテストで担保)")
	}

	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	holder, err := os.OpenFile(tempPathOf(out), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatalf("一時ファイルを準備できない: %v", err)
	}
	defer func() { _ = holder.Close() }()
	if err := tryLock(holder); err != nil {
		t.Fatalf("テスト側のロック取得に失敗した: %v", err)
	}
	defer func() { _ = unlock(holder) }()

	om, err := openOutput(newOutputConfig(out))
	if om != nil {
		t.Error("ロック済みの一時ファイルがあるのに OutputManager が返った")
	}
	if code := exitCodeOf(t, err); code != ExitOutput {
		t.Errorf("終了コードが %d。同時実行の排他は %d でなければならない", code, ExitOutput)
	}
	if !errors.Is(err, ErrLocked) {
		t.Errorf("ErrLocked が errors.Is で辿れない(%%w で包んでいない): %v", err)
	}
}

// TestOutputPathWithSpacesAndJapanese は、空白と日本語を含む絶対パスで
// 新規作成が成立することを検証する(要件 4.1 / FR-20)。
func TestOutputPathWithSpacesAndJapanese(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "出力 先 ディレクトリ")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("日本語ディレクトリを作成できない: %v", err)
	}
	out := filepath.Join(dir, "取込 結果 データ.ndjson")
	if !filepath.IsAbs(out) {
		t.Fatalf("テストの前提が崩れている(絶対パスではない): %q", out)
	}

	om := mustOpenOutput(t, newOutputConfig(out))
	writeRecords(t, om, `{"名前":"山田 太郎"}`)
	if err := om.Finalize(); err != nil {
		t.Fatalf("Finalize が失敗した: %v", err)
	}

	const want = "{\"名前\":\"山田 太郎\"}\n"
	if got := readOutput(t, out); string(got) != want {
		t.Errorf("内容が期待と異なる\n got=%q\nwant=%q", got, want)
	}
	assertNotExist(t, tempPathOf(out), "一時ファイル")
}

// TestAbortAfterFinalizeKeepsCommittedFile は、確定に成功した後で Abort が
// 呼ばれても確定済みの出力を壊さないことを検証する。
//
// main は `defer om.Abort()` で異常終了経路を守るため、正常系でも Finalize の
// 直後に Abort が走りうる(design.md「output」の Preconditions は排他呼び出しを
// 前提とするが、defer による後追いの Abort は現実に起こる)。ここで一時ファイル
// 削除ロジックが無条件に走ると、確定済みの出力を消す事故になりうる。
func TestAbortAfterFinalizeKeepsCommittedFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	om := mustOpenOutput(t, newOutputConfig(out))
	writeRecords(t, om, `{"a":1}`)
	if err := om.Finalize(); err != nil {
		t.Fatalf("Finalize が失敗した: %v", err)
	}

	om.Abort()
	om.Abort() // 二重呼び出しでも壊れないこと

	const want = "{\"a\":1}\n"
	if got := readOutput(t, out); string(got) != want {
		t.Errorf("確定後の Abort で出力が壊れた\n got=%q\nwant=%q", got, want)
	}
}

// TestFinalizeAfterAbortDoesNotCreateOutput は、中断後に Finalize が呼ばれても
// 出力パスにファイルを作らないことを検証する(状態機械の逆方向の保護)。
func TestFinalizeAfterAbortDoesNotCreateOutput(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	om := mustOpenOutput(t, newOutputConfig(out))
	writeRecords(t, om, `{"a":1}`)
	om.Abort()

	if err := om.Finalize(); err != nil {
		t.Fatalf("中断後の Finalize は無操作であるべきだが失敗した: %v", err)
	}
	assertNotExist(t, out, "中断後に Finalize した出力ファイル")
}

// TestWriteRecordAfterFinalizeFails は、確定後の書き込みが拒否されることを検証する。
// 見逃すと、確定済み出力とは無関係な場所へ黙って書き込む経路が残る。
func TestWriteRecordAfterFinalizeFails(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	om := mustOpenOutput(t, newOutputConfig(out))
	if err := om.Finalize(); err != nil {
		t.Fatalf("Finalize が失敗した: %v", err)
	}

	if err := om.WriteRecord(jsontext.Value(`{"a":1}`)); err == nil {
		t.Fatal("確定後の WriteRecord が成功として報告された")
	}
	if got := readOutput(t, out); len(got) != 0 {
		t.Errorf("確定後の書き込みが出力へ反映された: %q", got)
	}
}

// TestWriteRecordDoesNotRetainCallerBuffer は、WriteRecord が受け取ったバイト列を
// 呼び出しを越えて保持しないことを検証する(tasks.md「未配線の契約」、
// convert.go の RecordSink の寿命契約)。
//
// convert はレコードのバッファを再利用するため、保持する実装では出力が
// 「最後のレコードの繰り返し」や破損した行になる。ここでは呼び出し後に
// 同じバッファを書き換え、出力が影響を受けないことを確認する。
func TestWriteRecordDoesNotRetainCallerBuffer(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	om := mustOpenOutput(t, newOutputConfig(out))

	buf := []byte(`{"a":1}`)
	if err := om.WriteRecord(jsontext.Value(buf)); err != nil {
		t.Fatalf("WriteRecord が失敗した: %v", err)
	}
	// convert がバッファを再利用する様子を模して、同じ領域を上書きする。
	copy(buf, []byte(`{"a":9}`))
	if err := om.WriteRecord(jsontext.Value(buf)); err != nil {
		t.Fatalf("2 件目の WriteRecord が失敗した: %v", err)
	}
	copy(buf, []byte(`{"z":0}`))

	if err := om.Finalize(); err != nil {
		t.Fatalf("Finalize が失敗した: %v", err)
	}

	const want = "{\"a\":1}\n{\"a\":9}\n"
	if got := readOutput(t, out); string(got) != want {
		t.Errorf("呼び出し後のバッファ変更が出力に影響した(スライスを保持している)\n got=%q\nwant=%q", got, want)
	}
}

// TestOpenOutputFailsWhenDirectoryMissing は、出力先ディレクトリが存在しない場合に
// 終了コード 4 でパス付きのエラーになることを検証する
// (design.md「Error Handling」: 出力書き込みエラー=4、stderr にパスを含む理由)。
func TestOpenOutputFailsWhenDirectoryMissing(t *testing.T) {
	out := filepath.Join(t.TempDir(), "存在しない", "out.ndjson")

	om, err := openOutput(newOutputConfig(out))
	if om != nil {
		t.Error("ディレクトリがないのに OutputManager が返った")
	}
	if code := exitCodeOf(t, err); code != ExitOutput {
		t.Errorf("終了コードが %d。出力書き込みエラーは %d でなければならない", code, ExitOutput)
	}
	if !strings.Contains(err.Error(), out) {
		t.Errorf("エラーメッセージに出力パスが含まれない: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 追記モード(タスク 3.3)
//
// タスク 3.2 が置いていた見張り番 TestOpenOutputAppendModeIsNotWiredYet
// (追記が未結線であることを固定するテスト)は、以下の本来のテスト群で
// 置き換えた(tasks.md「未配線の契約」の指示)。
// ---------------------------------------------------------------------------

// newAppendConfig は追記モード(--append)の最小構成を返す。
func newAppendConfig(out string) *Config {
	cfg := newOutputConfig(out)
	cfg.Append = true
	return cfg
}

// writeExisting は追記先の既存ファイルを用意する。
func writeExisting(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("既存ファイル %q を準備できない: %v", path, err)
	}
}

// bigRecord は書き込みバッファ(writeBufferSize)を必ず超える大きさのレコードを返す。
// bufio.Writer はバッファに収まらない書き込みを下位ファイルへ直接流すため、これを
// 書くと追記が実際にファイルへ届き、ファイルが開始前サイズより大きくなる。
// ロールバックのテストは「本当に伸びたファイルを戻す」ことを確かめる必要がある。
func bigRecord(tag string) string {
	return `{"` + tag + `":"` + strings.Repeat("x", 2*writeBufferSize) + `"}`
}

// TestFinalizeAppendWritesExactBytes は、追記モードの結果バイト列を
// 「既存ファイルの末尾(改行あり/なし/空/不在)」×「改行コード(要件 3.3)」×
// 「最終行改行(要件 3.5)」×「追記件数 0/1/N」の組み合わせで固定する。
//
// 要件 4.3(既存がなければ新規作成して追記扱い)、要件 4.5(末尾が改行で
// 終わっていなければ改行を補ってから追記)をバイト単位で押さえる。
// 改行を補わない実装は「行の連結」として、逆に無条件で補う実装は「空行の挿入」として、
// ここで必ず落ちる。
func TestFinalizeAppendWritesExactBytes(t *testing.T) {
	tests := []struct {
		name     string
		noFile   bool // 追記先が存在しない(要件 4.3)
		existing string
		newline  string
		trailing bool
		records  []string
		want     string
	}{
		{
			name: "末尾が改行でない既存へ 1 件・LF・最終行改行あり",
			// 補完を怠ると `{"旧":1}{"新":1}` という 1 行に連結された不正な NDJSON になる。
			existing: `{"旧":1}`, newline: "\n", trailing: true,
			records: []string{`{"新":1}`},
			want:    "{\"旧\":1}\n{\"新\":1}\n",
		},
		{
			name:     "末尾が改行の既存へ 1 件・LF・最終行改行あり(空行を挿入しない)",
			existing: "{\"旧\":1}\n", newline: "\n", trailing: true,
			records: []string{`{"新":1}`},
			want:    "{\"旧\":1}\n{\"新\":1}\n",
		},
		{
			name:     "空(0 バイト)の既存へ 1 件(先頭に改行を置かない)",
			existing: "", newline: "\n", trailing: true,
			records: []string{`{"新":1}`},
			want:    "{\"新\":1}\n",
		},
		{
			name:   "既存ファイルがない場合は新規作成して追記扱い(要件 4.3)",
			noFile: true, newline: "\n", trailing: true,
			records: []string{`{"新":1}`},
			want:    "{\"新\":1}\n",
		},
		{
			name:     "末尾が改行でない既存へ 3 件・LF・最終行改行あり",
			existing: `{"旧":1}`, newline: "\n", trailing: true,
			records: []string{`{"a":1}`, `{"a":2}`, `{"a":3}`},
			want:    "{\"旧\":1}\n{\"a\":1}\n{\"a\":2}\n{\"a\":3}\n",
		},
		{
			name:     "末尾が改行でない既存へ 1 件・LF・最終行改行なし",
			existing: `{"旧":1}`, newline: "\n", trailing: false,
			records: []string{`{"新":1}`},
			want:    "{\"旧\":1}\n{\"新\":1}",
		},
		{
			name:     "末尾が改行でない既存へ 3 件・LF・最終行改行なし",
			existing: `{"旧":1}`, newline: "\n", trailing: false,
			records: []string{`{"a":1}`, `{"a":2}`, `{"a":3}`},
			want:    "{\"旧\":1}\n{\"a\":1}\n{\"a\":2}\n{\"a\":3}",
		},
		{
			name: "末尾が改行でない既存へ 2 件・CRLF(補完も CRLF)",
			// 補完に LF を使う実装はここで落ちる(要件 3.3 は行の改行コードを設定に従わせる)。
			existing: `{"旧":1}`, newline: "\r\n", trailing: true,
			records: []string{`{"a":1}`, `{"a":2}`},
			want:    "{\"旧\":1}\r\n{\"a\":1}\r\n{\"a\":2}\r\n",
		},
		{
			name:     "末尾が CRLF の既存へ 1 件・CRLF(補完しない)",
			existing: "{\"旧\":1}\r\n", newline: "\r\n", trailing: true,
			records: []string{`{"新":1}`},
			want:    "{\"旧\":1}\r\n{\"新\":1}\r\n",
		},
		{
			name:     "末尾が CRLF の既存へ 2 件・CRLF・最終行改行なし",
			existing: "{\"旧\":1}\r\n", newline: "\r\n", trailing: false,
			records: []string{`{"a":1}`, `{"a":2}`},
			want:    "{\"旧\":1}\r\n{\"a\":1}\r\n{\"a\":2}",
		},
		{
			name: "末尾が CR 単独の既存へ 1 件・LF(CR は行終端とみなさない)",
			// 末尾 1 バイトが '\r' のファイルは改行で終わっていない。補完しなければ
			// `{"旧":1}\r{"新":1}` となり、NDJSON の 1 行に 2 レコードが乗る。
			existing: "{\"旧\":1}\r", newline: "\n", trailing: true,
			records: []string{`{"新":1}`},
			want:    "{\"旧\":1}\r\n{\"新\":1}\n",
		},
		{
			name: "末尾が改行でない既存へ 0 件(1 バイトも変えない)",
			// 補完の改行は「既存の行と追記した行を分ける」ためのもの。追記する行が
			// なければ分けるものもなく、要件 4.5 の補完は発生しない(要件 3.10 の
			// 「0 件でも正常終了」を追記モードで具体化したもの)。
			existing: `{"旧":1}`, newline: "\n", trailing: true,
			records: nil,
			want:    `{"旧":1}`,
		},
		{
			name:     "末尾が改行でない既存へ 0 件・最終行改行なし(1 バイトも変えない)",
			existing: `{"旧":1}`, newline: "\n", trailing: false,
			records: nil,
			want:    `{"旧":1}`,
		},
		{
			name:   "既存ファイルがない場合の 0 件は 0 バイトのファイルになる",
			noFile: true, newline: "\n", trailing: true,
			records: nil,
			want:    "",
		},
		{
			name:     "日本語・絵文字・エスケープを含むレコードを素通しする",
			existing: `{"旧":1}`, newline: "\n", trailing: true,
			records: []string{`{"名前":"山田 太郎","絵文字":"😀","改行":"a\nb"}`},
			want:    "{\"旧\":1}\n{\"名前\":\"山田 太郎\",\"絵文字\":\"😀\",\"改行\":\"a\\nb\"}\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			out := filepath.Join(dir, "out.ndjson")
			if !tt.noFile {
				writeExisting(t, out, tt.existing)
			}

			cfg := newAppendConfig(out)
			cfg.Newline = tt.newline
			cfg.TrailingNewline = tt.trailing

			om := mustOpenOutput(t, cfg)
			writeRecords(t, om, tt.records...)
			if err := om.Finalize(); err != nil {
				t.Fatalf("Finalize が失敗した: %v", err)
			}

			got := readOutput(t, out)
			if string(got) != tt.want {
				t.Errorf("追記後のバイト列が期待と異なる\n got=%q\nwant=%q", got, tt.want)
			}
			if bytes.HasPrefix(got, utf8BOM) {
				t.Error("出力の先頭に BOM が書かれている(要件 3.4 は BOM なし UTF-8)")
			}
			// 追記モードは一時ファイルを使わない(出力ファイル自身をロックして追記する)。
			assertNotExist(t, tempPathOf(out), "一時ファイル")
		})
	}
}

// TestAbortAfterPartialAppendRestoresPreAppendSize は、追記の途中で中断したときに
// 既存ファイルが開始前のサイズ・内容へ戻ることを検証する
// (要件 4.4 / FR-23、要件 4.9 / FR-39、タスク 3.3 の完了条件、
// design.md「Testing Strategy / Integration Tests」2)。
//
// 中断は main の defer が担う経路そのもの(変換側の失敗=終了コード 2/3/5 など)を
// 模している。バッファを超える大きさのレコードを書くことで、中断前にバイトが
// 実際にファイルへ届いている状態を作る。届いていなければ切り詰めが無くても
// テストが通ってしまうため、伸びたこと自体もアサートする。
func TestAbortAfterPartialAppendRestoresPreAppendSize(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	const original = `{"既存":"末尾に改行がない行"}`
	writeExisting(t, out, original)

	om := mustOpenOutput(t, newAppendConfig(out))
	writeRecords(t, om, bigRecord("a"), bigRecord("b"))

	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("追記中のファイルを stat できない: %v", err)
	}
	if info.Size() <= int64(len(original)) {
		t.Fatalf("テストの前提が崩れている(追記がまだファイルへ届いていない): size=%d", info.Size())
	}

	om.Abort()

	got := readOutput(t, out)
	if string(got) != original {
		t.Errorf("中断後の内容が開始前と異なる(切り詰めが効いていない)\n size=%d want_size=%d\n got(先頭 80 バイト)=%q\nwant=%q",
			len(got), len(original), got[:min(len(got), 80)], original)
	}
}

// TestWriteRecordFailureDuringAppendRestoresFile は、追記中の書き込み失敗が
// 終了コード 4 で報告され、その後の中断で既存ファイルが開始前サイズへ戻ることを
// 検証する(要件 4.4 / FR-23、要件 4.9 / FR-39)。
//
// 失敗の注入はタスク 3.2 と同じ「下位のファイルハンドルを閉じてから、バッファを
// 超える大きさのレコードを書く」方式で、OS が返す本物のエラーを経路に通す。
// 閉じたあとはハンドル経由の切り詰めができないため、パス経由の切り詰めまで含めて
// 復元されることをここで固定する。
func TestWriteRecordFailureDuringAppendRestoresFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	const original = `{"既存":"この内容は失敗後も残らねばならない"}`
	writeExisting(t, out, original)

	om := mustOpenOutput(t, newAppendConfig(out))
	writeRecords(t, om, bigRecord("a"))

	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("追記中のファイルを stat できない: %v", err)
	}
	if info.Size() <= int64(len(original)) {
		t.Fatalf("テストの前提が崩れている(追記がまだファイルへ届いていない): size=%d", info.Size())
	}

	if err := om.file.Close(); err != nil {
		t.Fatalf("失敗注入のためのクローズができない: %v", err)
	}

	err = om.WriteRecord(jsontext.Value(bigRecord("b")))
	if err == nil {
		t.Fatal("閉じたファイルへの追記が成功として報告された")
	}
	if code := exitCodeOf(t, err); code != ExitOutput {
		t.Errorf("追記失敗の終了コードが %d。%d でなければならない", code, ExitOutput)
	}
	if !strings.Contains(err.Error(), out) {
		t.Errorf("エラーメッセージに出力パスが含まれない: %v", err)
	}

	om.Abort()

	if got := readOutput(t, out); string(got) != original {
		t.Errorf("書き込み失敗後の中断で開始前の状態へ戻っていない\n size=%d want_size=%d", len(got), len(original))
	}
}

// TestFinalizeFailureDuringAppendRestoresFile は、追記の確定手順(Flush→Sync→
// unlock→Close)の途中で失敗したときも、既存ファイルが開始前サイズへ戻ることを
// 検証する(要件 4.9 / FR-39)。確定に失敗したのに伸びたままのファイルを残す実装、
// 失敗を検出せず成功を返す実装はここで落ちる。
func TestFinalizeFailureDuringAppendRestoresFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	const original = "{\"既存\":\"確定失敗後も残る\"}\n"
	writeExisting(t, out, original)

	om := mustOpenOutput(t, newAppendConfig(out))
	writeRecords(t, om, bigRecord("a"))

	if err := om.file.Close(); err != nil {
		t.Fatalf("失敗注入のためのクローズができない: %v", err)
	}

	err := om.Finalize()
	if err == nil {
		t.Fatal("閉じたハンドルに対する Finalize が成功として報告された")
	}
	if code := exitCodeOf(t, err); code != ExitOutput {
		t.Errorf("確定失敗の終了コードが %d。%d でなければならない", code, ExitOutput)
	}
	if got := readOutput(t, out); string(got) != original {
		t.Errorf("確定失敗後に開始前の状態へ戻っていない\n size=%d want_size=%d", len(got), len(original))
	}

	// 確定に失敗した後は中断済み。main の defer が Abort を重ねても壊れない。
	om.Abort()
	if got := readOutput(t, out); string(got) != original {
		t.Errorf("確定失敗+中断の重ね掛けで内容が変わった\n got=%q\nwant=%q", got, original)
	}
}

// TestAbortAfterAppendFinalizeKeepsCommittedContent は、確定に成功した後で Abort が
// 呼ばれても、追記済みの内容が切り詰められないことを検証する。
//
// main は `defer om.Abort()` で異常終了経路を守るため、正常系でも Finalize の直後に
// Abort が走る。中断処理が状態を見ずに開始前サイズへ切り詰めると、確定した追記が
// 毎回消えるという最悪の事故になる。
func TestAbortAfterAppendFinalizeKeepsCommittedContent(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	const original = "{\"旧\":1}\n"
	writeExisting(t, out, original)

	om := mustOpenOutput(t, newAppendConfig(out))
	writeRecords(t, om, `{"新":1}`)
	if err := om.Finalize(); err != nil {
		t.Fatalf("Finalize が失敗した: %v", err)
	}

	om.Abort()
	om.Abort() // 二重呼び出しでも壊れないこと

	const want = "{\"旧\":1}\n{\"新\":1}\n"
	if got := readOutput(t, out); string(got) != want {
		t.Errorf("確定後の Abort で追記内容が失われた\n got=%q\nwant=%q", got, want)
	}
}

// TestAbortOnNewlyCreatedAppendTargetLeavesEmptyFile は、`--append` で新規作成した
// ファイルを中断したときの状態を固定する。
//
// 【判断】要件 4.9 / FR-39 は追記モードの復元先を「追記開始前のサイズ」と定める。
// 新規作成した場合その値は 0 であり、0 バイトへの切り詰めが規定どおりの復元である。
// ファイル自体の削除は行わない。理由は 2 つ:
//   - 要件 4.3 / FR-22 は「既存ファイルが存在しなければ新規作成する」と定めており、
//     ファイルの存在は追記モードが正規に作った開始前状態の一部である。
//   - 削除は「自分が作ったのか、開いた瞬間に他者が作ったのか」を区別できないまま
//     他者のデータを消しうる。タスク 3.2 の Abort が出力パスを削除しないと決めた
//     判断(tasks.md「タスク 3.2 が確定した output の契約」)と同じ理由である。
func TestAbortOnNewlyCreatedAppendTargetLeavesEmptyFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")

	om := mustOpenOutput(t, newAppendConfig(out))
	writeRecords(t, om, bigRecord("a"))
	om.Abort()

	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("追記モードで作成したファイルは中断後も残らねばならない: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("中断後のサイズが %d。開始前サイズ(0)でなければならない", info.Size())
	}
}

// TestAppendToLargeExistingFile は、大きな既存ファイル(末尾は改行なし)への追記が
// 正しく行われることを検証する。末尾判定のためにファイル全体を読む実装でも結果は
// 同じになるため、「末尾 1 バイトしか読まない」ことの証明は
// TestNeedsNewlinePaddingReadsOnlyLastByte が読み取り回数で行う。
// こちらは大きな既存内容が 1 バイトも書き換わらないことを担保する。
func TestAppendToLargeExistingFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	original := strings.Repeat("{\"旧\":\"0123456789012345678901234567890123456789\"}\n", 8192) +
		`{"旧":"末尾に改行がない行"}`
	writeExisting(t, out, original)

	om := mustOpenOutput(t, newAppendConfig(out))
	writeRecords(t, om, `{"新":1}`)
	if err := om.Finalize(); err != nil {
		t.Fatalf("Finalize が失敗した: %v", err)
	}

	want := original + "\n" + `{"新":1}` + "\n"
	got := readOutput(t, out)
	if string(got) != want {
		t.Errorf("大きな既存ファイルへの追記結果が期待と異なる(size got=%d want=%d)",
			len(got), len(want))
	}
}

// countingReaderAt は ReadAt の呼び出し回数・読み取りバイト数・オフセットを記録する
// io.ReaderAt。末尾判定が「ファイル全体を読む」実装へ退行したことを検出するために使う。
type countingReaderAt struct {
	data    []byte
	calls   int
	read    int
	offsets []int64
	// eofWithData を立てると、末尾まで読み切った呼び出しで (n, io.EOF) を返す。
	// io.ReaderAt はこの返し方を許しており(n == len(p) でも EOF を返してよい)、
	// これをエラー扱いする実装は追記を開始できなくなる。
	eofWithData bool
	// err を立てると常にこのエラーを返す(読み取り失敗の経路の検証用)。
	err error
}

func (c *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	c.calls++
	c.offsets = append(c.offsets, off)
	if c.err != nil {
		return 0, c.err
	}
	if off < 0 || off >= int64(len(c.data)) {
		return 0, io.EOF
	}
	n := copy(p, c.data[off:])
	c.read += n
	if c.eofWithData || n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// TestNeedsNewlinePaddingReadsOnlyLastByte は、末尾改行の判定が
//   - 正しい真偽値を返すこと
//   - 読み取るのが末尾 1 バイトだけであること(design.md「末尾 1 バイトのみ読む」、
//     要件 4.5 / FR-24)
//
// を検証する。300 MB 級の追記先を毎回読み直す実装への退行を、読み取りバイト数の
// アサーションで捕まえる。
func TestNeedsNewlinePaddingReadsOnlyLastByte(t *testing.T) {
	tests := []struct {
		name      string
		data      string
		want      bool
		wantCalls int
		wantRead  int
	}{
		{"末尾が } なら補完が必要", `{"a":1}`, true, 1, 1},
		{"末尾が LF なら補完は不要", "{\"a\":1}\n", false, 1, 1},
		{"末尾が CRLF でも最後の 1 バイトは LF なので不要", "{\"a\":1}\r\n", false, 1, 1},
		{"末尾が CR 単独なら補完が必要", "{\"a\":1}\r", true, 1, 1},
		{"0 バイトなら 1 バイトも読まずに不要と判定する", "", false, 0, 0},
		{"途中に改行があっても末尾だけで判定する", "a\nb\nc", true, 1, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &countingReaderAt{data: []byte(tt.data)}
			got, err := needsNewlinePadding(r, int64(len(tt.data)))
			if err != nil {
				t.Fatalf("needsNewlinePadding が失敗した: %v", err)
			}
			if got != tt.want {
				t.Errorf("判定が %v。%v でなければならない", got, tt.want)
			}
			if r.calls != tt.wantCalls {
				t.Errorf("ReadAt の呼び出しが %d 回。%d 回でなければならない", r.calls, tt.wantCalls)
			}
			if r.read != tt.wantRead {
				t.Errorf("読み取りが %d バイト。%d バイトでなければならない(末尾 1 バイトのみを読む)",
					r.read, tt.wantRead)
			}
			if tt.wantCalls > 0 {
				if want := int64(len(tt.data) - 1); r.offsets[0] != want {
					t.Errorf("読み取りオフセットが %d。末尾の %d でなければならない", r.offsets[0], want)
				}
			}
		})
	}
}

// TestNeedsNewlinePaddingAcceptsEOFWithData は、末尾 1 バイトを読み切った呼び出しが
// (1, io.EOF) を返す ReaderAt でも判定が成立することを検証する。
// io.ReaderAt の契約は n == len(p) のときに EOF を返すことを許しており、これを
// 失敗として扱うと追記が「末尾 1 バイトを読めない」で終了コード 4 になる。
func TestNeedsNewlinePaddingAcceptsEOFWithData(t *testing.T) {
	r := &countingReaderAt{data: []byte(`{"a":1}`), eofWithData: true}
	got, err := needsNewlinePadding(r, int64(len(r.data)))
	if err != nil {
		t.Fatalf("(n, io.EOF) を返す ReaderAt で失敗した: %v", err)
	}
	if !got {
		t.Error("末尾が } なので補完が必要と判定されねばならない")
	}
}

// TestNeedsNewlinePaddingReportsReadFailure は、末尾 1 バイトを読めない場合に
// エラーが握り潰されないことを検証する。ここで nil を返すと、読めなかっただけの
// ファイルに対して「改行あり」とみなして行を連結してしまう。
func TestNeedsNewlinePaddingReportsReadFailure(t *testing.T) {
	want := errors.New("読み取り失敗")
	r := &countingReaderAt{data: []byte(`{"a":1}`), err: want}
	if _, err := needsNewlinePadding(r, int64(len(r.data))); !errors.Is(err, want) {
		t.Errorf("読み取り失敗が伝播しない: %v", err)
	}
}

// TestOpenAppendRefusesLockedOutputFile は、同じ出力ファイルを処理中の別プロセスが
// いる場合に終了コード 4 で停止することを検証する(要件 4.6 / FR-25)。
// 追記モードのロック対象は一時ファイルではなく出力ファイル自身である
// (design.md「output」の「排他」)。
//
// 【この環境での限界】非 Windows の tryLock は no-op で常に成功するため、この分岐は
// 原理的に到達できない(tasks.md「未配線の契約」: タスク 3.3 は Linux 上で
// 「同時実行 → 終了コード 4」を検証済みと主張してはならない)。要件 4.6 の実体は
// Windows 受け入れテストで担保する。
func TestOpenAppendRefusesLockedOutputFile(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("非 Windows の tryLock は no-op のため、ロック済みの出力ファイルを再現できない(要件 4.6 は Windows 受け入れテストで担保)")
	}

	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	writeExisting(t, out, "{\"旧\":1}\n")

	holder, err := os.OpenFile(out, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("出力ファイルを開けない: %v", err)
	}
	defer func() { _ = holder.Close() }()
	if err := tryLock(holder); err != nil {
		t.Fatalf("テスト側のロック取得に失敗した: %v", err)
	}
	defer func() { _ = unlock(holder) }()

	om, err := openOutput(newAppendConfig(out))
	if om != nil {
		t.Error("ロック済みの出力ファイルがあるのに OutputManager が返った")
	}
	if code := exitCodeOf(t, err); code != ExitOutput {
		t.Errorf("終了コードが %d。同時実行の排他は %d でなければならない", code, ExitOutput)
	}
	if !errors.Is(err, ErrLocked) {
		t.Errorf("ErrLocked が errors.Is で辿れない(%%w で包んでいない): %v", err)
	}
}

// TestAppendPathWithSpacesAndJapanese は、空白と日本語を含む絶対パスへの追記が
// 成立することを検証する(要件 4.1 / FR-20)。
func TestAppendPathWithSpacesAndJapanese(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "出力 先 ディレクトリ")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("日本語ディレクトリを作成できない: %v", err)
	}
	out := filepath.Join(dir, "取込 結果 データ.ndjson")
	if !filepath.IsAbs(out) {
		t.Fatalf("テストの前提が崩れている(絶対パスではない): %q", out)
	}
	const original = `{"既存":"改行なし"}`
	writeExisting(t, out, original)

	om := mustOpenOutput(t, newAppendConfig(out))
	writeRecords(t, om, `{"名前":"山田 太郎"}`)
	if err := om.Finalize(); err != nil {
		t.Fatalf("Finalize が失敗した: %v", err)
	}

	want := original + "\n" + "{\"名前\":\"山田 太郎\"}\n"
	if got := readOutput(t, out); string(got) != want {
		t.Errorf("内容が期待と異なる\n got=%q\nwant=%q", got, want)
	}
}

// TestOpenAppendFailsWhenDirectoryMissing は、追記先のディレクトリが存在しない場合に
// 終了コード 4 でパス付きのエラーになることを検証する
// (design.md「Error Handling」: 出力書き込みエラー=4、stderr にパスを含む理由)。
func TestOpenAppendFailsWhenDirectoryMissing(t *testing.T) {
	out := filepath.Join(t.TempDir(), "存在しない", "out.ndjson")

	om, err := openOutput(newAppendConfig(out))
	if om != nil {
		t.Error("ディレクトリがないのに OutputManager が返った")
	}
	if code := exitCodeOf(t, err); code != ExitOutput {
		t.Errorf("終了コードが %d。出力書き込みエラーは %d でなければならない", code, ExitOutput)
	}
	if !strings.Contains(err.Error(), out) {
		t.Errorf("エラーメッセージに出力パスが含まれない: %v", err)
	}
}

// TestRunConversionAppendsThroughOutputManager は、実際の変換器(convert)を
// 書き込み元に据えた追記が、既存内容を保ったまま golden と一致することを検証する
// (design.md「Testing Strategy / Integration Tests」2)。
//
// convert は同じバッファを使い回してレコードを渡してくる(tasks.md「未配線の契約」の
// 寿命契約)。追記経路でもそのスライスを保持していないことがここで露見する。
func TestRunConversionAppendsThroughOutputManager(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	const original = `{"既存":"改行なしで終わる"}`
	writeExisting(t, out, original)

	cfg := newAppendConfig(out)
	cfg.Inputs = []string{fixture("numbers.json")}

	om := mustOpenOutput(t, cfg)
	res, err := runConversion(cfg, om, nil)
	if err != nil {
		t.Fatalf("runConversion が失敗した: %v", err)
	}
	if err := om.Finalize(); err != nil {
		t.Fatalf("Finalize が失敗した: %v", err)
	}

	golden, err := os.ReadFile(fixture("numbers.ndjson"))
	if err != nil {
		t.Fatalf("golden を読めない: %v", err)
	}
	want := append([]byte(original+"\n"), golden...)
	if got := readOutput(t, out); !bytes.Equal(got, want) {
		t.Errorf("追記結果が期待と異なる\n got=%q\nwant=%q", got, want)
	}
	if res.Records != 11 {
		t.Errorf("出力件数が %d。golden の行数と一致しなければならない", res.Records)
	}
}

// TestOutputManagerSatisfiesRecordSinkAtRuntime は、OutputManager を
// RecordSink として渡した書き込みが実ファイルへ届くことを検証する。
// コンパイル時アサーション(ファイル冒頭)だけでは、インターフェース経由の
// 呼び出しが実際に書き込みを行うかまでは保証できない。
func TestOutputManagerSatisfiesRecordSinkAtRuntime(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	om := mustOpenOutput(t, newOutputConfig(out))

	var sink RecordSink = om
	if err := sink.WriteRecord(jsontext.Value(`{"a":1}`)); err != nil {
		t.Fatalf("RecordSink 経由の WriteRecord が失敗した: %v", err)
	}
	if err := om.Finalize(); err != nil {
		t.Fatalf("Finalize が失敗した: %v", err)
	}

	const want = "{\"a\":1}\n"
	if got := readOutput(t, out); string(got) != want {
		t.Errorf("RecordSink 経由の書き込み結果が期待と異なる\n got=%q\nwant=%q", got, want)
	}
}

// TestRunConversionThroughOutputManager は、実際の変換器(convert)を書き込み元に
// 据えて、ファイル層まで通した結果が golden とバイト単位で一致することを検証する
// (design.md「Testing Strategy / Integration Tests」1、要件 3.4 / 3.7〜3.9)。
//
// 単体テストの手書きレコードと違い、ここでは convert が「同じバッファを使い回して」
// レコードを渡してくる(tasks.md「未配線の契約」の寿命契約)。書き込み側がその
// スライスを保持していれば、golden との差としてここで露見する。
func TestRunConversionThroughOutputManager(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	cfg := newOutputConfig(out)
	cfg.Inputs = []string{fixture("numbers.json")}

	om := mustOpenOutput(t, cfg)
	res, err := runConversion(cfg, om, nil)
	if err != nil {
		t.Fatalf("runConversion が失敗した: %v", err)
	}
	if err := om.Finalize(); err != nil {
		t.Fatalf("Finalize が失敗した: %v", err)
	}

	want, err := os.ReadFile(fixture("numbers.ndjson"))
	if err != nil {
		t.Fatalf("golden を読めない: %v", err)
	}
	if got := readOutput(t, out); !bytes.Equal(got, want) {
		t.Errorf("出力が golden と一致しない\n got=%q\nwant=%q", got, want)
	}
	if res.Records != 11 {
		t.Errorf("出力件数が %d。golden の行数と一致しなければならない", res.Records)
	}
}

// TestRunConversionEmptyArrayCreatesEmptyFile は、空配列を変換したときに
// レコードを 1 件も書かず 0 バイトの出力ファイルが残ることを、変換器から
// ファイル層まで通して検証する(要件 3.10 / FR-19)。
//
// tasks.md「タスク 2.4 が常設したテスト資産」が、この「0 バイトのファイルを作成する」
// 観点をファイル層の責務としてタスク 3.2 へ明示的に委譲している。
func TestRunConversionEmptyArrayCreatesEmptyFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	cfg := newOutputConfig(out)
	cfg.Inputs = []string{fixture("empty.json")}
	ptr, err := ParsePointer("/data")
	if err != nil {
		t.Fatalf("ParsePointer が失敗した: %v", err)
	}
	cfg.Path = ptr

	om := mustOpenOutput(t, cfg)
	res, err := runConversion(cfg, om, nil)
	if err != nil {
		t.Fatalf("runConversion が失敗した: %v", err)
	}
	if res.Records != 0 {
		t.Fatalf("空配列なのに %d 件出力された", res.Records)
	}
	if err := om.Finalize(); err != nil {
		t.Fatalf("Finalize が失敗した: %v", err)
	}

	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("空配列でも出力ファイルは作成されねばならない: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("空配列の出力が %d バイト。0 バイトでなければならない", info.Size())
	}
}

// TestOpenOutputDefaultsNewlineToLF は、Config.Newline がゼロ値のときに
// 既定の LF が使われることを検証する(要件 3.3 / FR-12 の既定は LF)。
// 区切りが空文字列になると、複数レコードが 1 行へ連結された不正な NDJSON になる。
func TestOpenOutputDefaultsNewlineToLF(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	cfg := &Config{Out: out, TrailingNewline: true} // Newline は未設定

	om := mustOpenOutput(t, cfg)
	writeRecords(t, om, `{"a":1}`, `{"a":2}`)
	if err := om.Finalize(); err != nil {
		t.Fatalf("Finalize が失敗した: %v", err)
	}

	const want = "{\"a\":1}\n{\"a\":2}\n"
	if got := readOutput(t, out); string(got) != want {
		t.Errorf("既定の改行が LF になっていない\n got=%q\nwant=%q", got, want)
	}
}
