package main

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
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

// TestOpenOutputAppendModeIsNotWiredYet は、追記モードがこのタスクの範囲外で
// あることを明示的に固定する見張り番である。
//
// タスク 3.2 は新規作成モードのみを実装する。追記モード(タスク 3.3)と
// ジャーナル復旧(タスク 3.4)を結線したら、このテストは失敗するので、
// そのとき追記モードの本来のテストへ置き換えること。
// 未実装の経路が「出力書き込みエラー(4)」を装って呼び出し元のリトライ判断を
// 誤らせないよう、終了コードは内部エラー(9)であることも固定する。
func TestOpenOutputAppendModeIsNotWiredYet(t *testing.T) {
	dir := t.TempDir()
	cfg := newOutputConfig(filepath.Join(dir, "out.ndjson"))
	cfg.Append = true

	om, err := openOutput(cfg)
	if om != nil {
		t.Fatal("追記モードは未実装だが OutputManager が返った(タスク 3.3 を実装したらこのテストを置き換える)")
	}
	if code := exitCodeOf(t, err); code != ExitInternal {
		t.Errorf("未結線の追記モードの終了コードが %d。内部エラー %d でなければならない", code, ExitInternal)
	}
	// 未実装の経路がファイルへ触れていないこと。
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("出力ディレクトリを列挙できない: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("未結線の追記モードがファイルを作成した: %v", entries)
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
