package main

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"fmt"
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

// TestAbortTruncatesThroughOpenHandle は、ロールバックの切り詰めが
// **開いたままのハンドル経由**で行われることを検証する(要件 4.4 / FR-23、4.9 / FR-39)。
//
// これは性能の話ではなく排他の話である。ハンドル経由の切り詰めはロックを保持したまま
// 完了するが、パス経由の os.Truncate は closeFile がロックを解放した後に走るため、
// 別プロセスがロックを取得して追記を始めた直後であればその追記ごと切り詰めうる。
// パス経由はあくまで最後の手段であって、通常の中断経路であってはならない。
//
// 「ハンドル経由かパス経由か」は結果のバイト列では区別できない。そこで中断の直前に
// 出力ファイルを別名へ移す。ハンドルは inode を掴んでいるので切り詰めが届くが、
// パス経由の os.Truncate は元のパスに何も無いため失敗する。移した先が開始前サイズに
// 戻っていれば、ハンドル経由が実際に効いたことの証明になる。
//
// Windows は開いているファイルの移動を許さない(共有モードに FILE_SHARE_DELETE を
// 含めていない)ため、この手法は非 Windows 限定である。検証している不変条件そのものは
// Windows のためのものだが、退行はここで捕まえられる。
func TestAbortTruncatesThroughOpenHandle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("開いているファイルを移動できないため、この手法は Windows では使えない")
	}

	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	moved := filepath.Join(dir, "moved.ndjson")
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

	// ここから先、cfg.Out のパスには何も存在しない。パス経由の切り詰めは届かない。
	if err := os.Rename(out, moved); err != nil {
		t.Fatalf("追記中のファイルを移動できない: %v", err)
	}

	om.Abort()

	assertNotExist(t, out, "移動した後の出力パス")

	got := readOutput(t, moved)
	if string(got) != original {
		t.Errorf("ハンドル経由の切り詰めが効いていない(パス経由の os.Truncate に頼っている)。"+
			"ロールバックはロックを保持したまま完了しなければならない\n size=%d want_size=%d",
			len(got), len(original))
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

// TestAppendOpenFlagsAreWindowsSafe は、追記先を開くフラグが Windows 固有の 2 つの
// 罠を踏んでいないことを、フラグの値そのもので見張る。
//
// この 2 つはどちらも非 Windows では一切症状が出ない(ロックは no-op、ftruncate は
// O_APPEND の fd でも成功する)。実行によるテストも `GOOS=windows go vet` も
// クロスコンパイルも捕まえられないため、値の検査だけが Linux 側に残された防御になる。
//
//   - O_APPEND があると、Windows のハンドルは FILE_WRITE_DATA を失う
//     (syscall_windows.go:386-395)。os.File.Truncate は
//     SetFileInformationByHandle(FileEndOfFileInfo) 経由でそれを要求するため、
//     rollbackAppend のハンドル経由の切り詰めが必ず失敗し、ロックを解放した後の
//     パス経由の切り詰めへ落ちる。すなわち中断のたびに、他プロセスがロックを
//     取得しうる窓の中で切り詰めることになる(要件 4.4 / FR-23、4.9 / FR-39)。
//   - O_RDWR でないと、末尾 1 バイトの読み取り(要件 4.5 / FR-24)ができないうえ、
//     Windows では LockFileEx が要求するアクセス権を失う(要件 4.6 / FR-25)。
func TestAppendOpenFlagsAreWindowsSafe(t *testing.T) {
	if appendOpenFlags&os.O_APPEND != 0 {
		t.Error("追記先を O_APPEND で開いている。Windows でハンドル経由の Truncate が" +
			"必ず失敗し、ロック解放後のパス経由の切り詰めへ落ちる")
	}
	if appendOpenFlags&os.O_RDWR == 0 {
		t.Error("追記先が O_RDWR で開かれていない。末尾 1 バイトを読めず、" +
			"Windows では LockFileEx も失敗する")
	}
	if appendOpenFlags&os.O_TRUNC != 0 {
		t.Error("追記先を O_TRUNC で開いている。既存の内容を破棄してしまう")
	}
	if appendOpenFlags&os.O_CREATE == 0 {
		t.Error("追記先が O_CREATE で開かれていない。既存ファイルがない場合に" +
			"新規作成する要件 4.3 / FR-22 を満たせない")
	}
}

// TestPrepareAppendStopsWhenPaddingProbeFails は、末尾 1 バイトの読み取りが失敗した
// ときに追記の準備そのものが停止することを、**呼び出し側**で検証する
// (要件 4.5 / FR-24、要件 4.3 / FR-22 の準備段)。
//
// TestNeedsNewlinePaddingReportsReadFailure は needsNewlinePadding が
// エラーを返すことしか縛らない。呼び出し側が `pad, _ := ...` と受けてエラーを捨てても
// あの 1 本は通る。そのとき pad は false になり、末尾が改行で終わっていない追記先へ
// 改行を補わずに書き足す — 完了条件が禁じている「行の連結」そのものが起きる。
// この 1 本だけが、その退行を落とす。
//
// 実ファイルの ReadAt を確実に失敗させる手立てが(root 実行下では権限操作を含めて)
// ないため、prepareAppend は末尾読み取り先を引数で受ける。本番の openAppend は
// 開いたファイル自身を渡すので、経路は同じで間接も増えない。
func TestPrepareAppendStopsWhenPaddingProbeFails(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	// 末尾が改行で終わっていない既存ファイル。握り潰した場合に実害
	//(行の連結)が出るのはこの形である。
	const original = `{"旧":1}`
	writeExisting(t, out, original)

	f, err := os.OpenFile(out, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatalf("追記先を開けない: %v", err)
	}

	wantErr := errors.New("末尾を読めない")
	probe := &countingReaderAt{data: []byte(original), err: wantErr}

	om, err := prepareAppend(newAppendConfig(out), f, probe)
	if err == nil {
		om.Abort()
		t.Fatal("末尾 1 バイトを読めないのに準備が成功した(エラーを握り潰すと、" +
			"改行を補わずに追記して行を連結する)")
	}
	if om != nil {
		t.Error("失敗したのに OutputManager を返している")
	}
	if got := exitCodeOf(t, err); got != ExitOutput {
		t.Errorf("終了コードが %d。%d でなければならない", got, ExitOutput)
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("読み取り失敗の原因が伝播していない: %v", err)
	}
	if !strings.Contains(err.Error(), out) {
		t.Errorf("エラーに追記先のパスが含まれない: %v", err)
	}
	if probe.calls != 1 {
		t.Errorf("末尾判定の ReadAt が %d 回。1 回でなければならない", probe.calls)
	}

	// 失敗経路はロックを解放してハンドルを閉じなければならない。閉じ忘れると
	// Windows では以後だれもこの出力ファイルを扱えなくなる。
	if err := f.Close(); !errors.Is(err, fs.ErrClosed) {
		t.Errorf("失敗経路がハンドルを閉じていない(再クローズの結果が %v)", err)
	}

	// 準備に失敗したのだから、既存ファイルは 1 バイトも変わっていてはならない。
	if got := readOutput(t, out); string(got) != original {
		t.Errorf("既存ファイルが書き換わっている: %q", got)
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

// ---------------------------------------------------------------------------
// ジャーナルによる強制終了後復旧(タスク 3.4、要件 4.8 / NFR-07、要件 4.9 / FR-39)
// ---------------------------------------------------------------------------

// journalPathFor はテストが期待するジャーナルのパス
// (design.md「Data Models / ジャーナルファイル(復旧契約)」の `<出力パス>.journal`)。
// 実装側の定数を参照せず文字列で組み立てるのは、パスの契約そのものを見張るためである
// (tempPathOf と同じ規律)。ジャーナルの名前と配置は design.md
// 「Revalidation Triggers」に載る外部契約であり、黙って変えてはならない。
func journalPathFor(out string) string { return out + ".journal" }

// wantJournalBytes は、design.md「Data Models」が定めるジャーナルの内容
// (`size=<10進数>` + LF、ASCII のみ)を組み立てる。
func wantJournalBytes(size int64) string { return fmt.Sprintf("size=%d\n", size) }

// assertJournalBytes は、ジャーナルの内容がバイト単位で規定どおりであることを検証する。
// 「サイズさえ合っていればよい」実装(JSON、テキスト+空白、改行なし、10 進数以外)への
// 逸脱をここで落とす。ジャーナルは別プロセス(次回起動)が読む唯一の受け渡し形式であり、
// 形式は実装内部の自由ではない。
func assertJournalBytes(t *testing.T, out string, wantSize int64) {
	t.Helper()
	got, err := os.ReadFile(journalPathFor(out))
	if err != nil {
		t.Fatalf("ジャーナル %q を読めない: %v", journalPathFor(out), err)
	}
	if want := wantJournalBytes(wantSize); string(got) != want {
		t.Errorf("ジャーナルの内容が %q。%q でなければならない", got, want)
	}
	for i, b := range got {
		if b >= 0x80 {
			t.Errorf("ジャーナルの %d バイト目が ASCII 範囲外(0x%02X)。ASCII 1 行でなければならない", i, b)
			break
		}
	}
}

// TestJournalIsSyncedBeforeFirstAppendedByte は、design.md「output」の不変条件
// 「ジャーナルが sync される前に追記の 1 バイト目を書かない」を、開いた直後の
// ディスク上の状態で検証する(要件 4.8 / NFR-07)。
//
// openOutput から戻った時点で、(a) ジャーナルが存在し開始前サイズを記録している、
// (b) 出力ファイルは 1 バイトも変わっていない、の両方が成り立たねばならない。
// ジャーナルを最初の WriteRecord まで遅らせる実装、末尾改行の補完を openAppend で
// 先に書いてしまう実装(タスク 3.3 がわざわざ WriteRecord まで遅らせた理由)は
// ここで落ちる。
func TestJournalIsSyncedBeforeFirstAppendedByte(t *testing.T) {
	tests := []struct {
		name     string
		noFile   bool
		existing string
	}{
		{name: "末尾が改行でない既存(補完の改行=追記の 1 バイト目が発生する)", existing: `{"旧":1}`},
		{name: "末尾が改行の既存", existing: "{\"旧\":1}\n"},
		{name: "空(0 バイト)の既存", existing: ""},
		{name: "既存ファイルなし(要件 4.3 で新規作成)", noFile: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			out := filepath.Join(dir, "out.ndjson")
			if !tt.noFile {
				writeExisting(t, out, tt.existing)
			}

			om := mustOpenOutput(t, newAppendConfig(out))
			defer om.Abort()

			// (a) ジャーナルは開いた直後に、開始前サイズを載せて存在していなければならない。
			assertJournalBytes(t, out, int64(len(tt.existing)))

			// (b) この時点で出力ファイルは 1 バイトも変わっていてはならない。
			if got := readOutput(t, out); string(got) != tt.existing {
				t.Errorf("ジャーナルの作成前後で出力ファイルが変わっている\n got=%q\nwant=%q", got, tt.existing)
			}

			// ディレクトリに現れてよいのは出力ファイルとジャーナルだけ
			// (要件 11.4 / NFR-13: 一時的に作るファイルは出力と同じディレクトリ)。
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("出力ディレクトリを列挙できない: %v", err)
			}
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			want := []string{filepath.Base(out), filepath.Base(journalPathFor(out))}
			if len(names) != len(want) || names[0] != want[0] || names[1] != want[1] {
				t.Errorf("追記中のディレクトリ内容が %v。%v でなければならない", names, want)
			}
		})
	}
}

// TestJournalIsRemovedOnFinalizeAndAbort は、正常確定と自発的な中断の双方で
// ジャーナルが消えることを検証する(タスク 3.4 の 1 つ目の箇条書き)。
//
// ジャーナルの存在は「前回の追記が未確定」を意味する不変条件そのものである
// (design.md「State Management」)。消し忘れると、次回実行が確定済みの内容を
// 開始前サイズへ切り詰めてしまう。
func TestJournalIsRemovedOnFinalizeAndAbort(t *testing.T) {
	const original = "{\"旧\":1}\n"

	t.Run("正常確定(Finalize)", func(t *testing.T) {
		dir := t.TempDir()
		out := filepath.Join(dir, "out.ndjson")
		writeExisting(t, out, original)

		om := mustOpenOutput(t, newAppendConfig(out))
		writeRecords(t, om, `{"新":1}`)
		if err := om.Finalize(); err != nil {
			t.Fatalf("Finalize が失敗した: %v", err)
		}

		assertNotExist(t, journalPathFor(out), "確定後のジャーナル")
		if got, want := string(readOutput(t, out)), original+"{\"新\":1}\n"; got != want {
			t.Errorf("確定後の内容が期待と異なる\n got=%q\nwant=%q", got, want)
		}
	})

	t.Run("自発的な中断(Abort)", func(t *testing.T) {
		dir := t.TempDir()
		out := filepath.Join(dir, "out.ndjson")
		writeExisting(t, out, original)

		om := mustOpenOutput(t, newAppendConfig(out))
		writeRecords(t, om, bigRecord("a"))
		om.Abort()

		assertNotExist(t, journalPathFor(out), "中断後のジャーナル")
		if got := string(readOutput(t, out)); got != original {
			t.Errorf("中断後の内容が開始前と異なる\n size=%d want_size=%d", len(got), len(original))
		}
	})

	t.Run("0 件の追記でも確定でジャーナルは消える", func(t *testing.T) {
		dir := t.TempDir()
		out := filepath.Join(dir, "out.ndjson")
		writeExisting(t, out, original)

		om := mustOpenOutput(t, newAppendConfig(out))
		if err := om.Finalize(); err != nil {
			t.Fatalf("Finalize が失敗した: %v", err)
		}

		assertNotExist(t, journalPathFor(out), "0 件確定後のジャーナル")
		if got := string(readOutput(t, out)); got != original {
			t.Errorf("0 件の追記でファイルが変わった\n got=%q\nwant=%q", got, original)
		}
	})
}

// TestAppendRecoversFromSimulatedHardKill は、タスク 3.4 の完了条件そのものである。
//
// 「ジャーナル作成後・追記中・切り詰め前」の各時点で強制終了された場合に
// ディスクへ残る状態を再現し、次回実行が開始前状態へ復旧してから追記することを
// バイト単位で検証する(要件 4.8 / NFR-07、design.md「Testing Strategy /
// Integration Tests」3)。
//
// 単体テストではプロセスを実際に強制終了できないため、強制終了が残す
// 「出力ファイル+ジャーナル」の組み合わせを直接作って次回実行を走らせる。
// 3 つの時点はいずれも「ジャーナルが存在し、ファイルが記録サイズ以上に伸びている
// (または等しい)」という同じ形に落ちるが、伸び方が異なる(0 バイト・途中で切れた行・
// 完全な行)ため、切り詰め先を取り違える実装の落ち方が変わる。
func TestAppendRecoversFromSimulatedHardKill(t *testing.T) {
	const base = `{"既存":"改行なしで終わる行"}`
	baseSize := int64(len(base))

	tests := []struct {
		name   string
		onDisk string
	}{
		{
			name: "ジャーナル作成後・追記の 1 バイト目を書く前に強制終了",
			// ファイルは開始前のまま。復旧は「何もしない切り詰め」に落ち着く。
			onDisk: base,
		},
		{
			name: "追記中に強制終了(途中で切れた行が残る)",
			// 補完の改行と書きかけのレコードがファイルへ届いた状態。
			onDisk: base + "\n" + `{"途中":123`,
		},
		{
			name: "ロールバックの切り詰め前に強制終了(完全な行まで書けている)",
			// 中断処理が切り詰めを行う直前に消えた状態。行としては閉じているため、
			// 「壊れていないから残してよい」と判断する実装はここで落ちる。
			// 呼び出し元はこの実行の成功を知らされていない以上、追記は無かったことに
			// しなければ再実行で重複する(要件 10.1 の冪等性)。
			onDisk: base + "\n" + `{"a":1}` + "\n" + `{"b":2}` + "\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			out := filepath.Join(dir, "out.ndjson")
			writeExisting(t, out, tt.onDisk)
			writeExisting(t, journalPathFor(out), wantJournalBytes(baseSize))

			om := mustOpenOutput(t, newAppendConfig(out))

			// 復旧はロック取得後・Stat より前に行われねばならない(design.md
			// 「フロー上の決定事項」、tasks.md「タスク 3.3 が確定した output の契約」)。
			// 順序を取り違えると、復旧前の誤ったサイズが開始前サイズとして記録される。
			if om.baseSize != baseSize {
				t.Errorf("開始前サイズが %d。復旧後の %d でなければならない"+
					"(復旧を Stat より後に置くとこうなる)", om.baseSize, baseSize)
			}
			// 復旧は「処理を始める前」に完了していなければならない(要件 4.8)。
			info, err := os.Stat(out)
			if err != nil {
				t.Fatalf("復旧後のファイルを stat できない: %v", err)
			}
			if info.Size() != baseSize {
				t.Errorf("復旧後のファイルサイズが %d。%d でなければならない", info.Size(), baseSize)
			}
			// 復旧に使ったジャーナルは削除され、この実行のジャーナルに置き換わっている。
			assertJournalBytes(t, out, baseSize)

			writeRecords(t, om, `{"新":1}`)
			if err := om.Finalize(); err != nil {
				t.Fatalf("Finalize が失敗した: %v", err)
			}

			want := base + "\n" + `{"新":1}` + "\n"
			if got := readOutput(t, out); string(got) != want {
				t.Errorf("復旧後の追記結果が期待と異なる\n got=%q\nwant=%q", got, want)
			}
			assertNotExist(t, journalPathFor(out), "確定後のジャーナル")
		})
	}
}

// TestRecoveredJournalIsDeletedBeforeNextJournalIsWritten は、復旧が残存ジャーナルを
// 確実に削除することを、「削除しなければ次の実行が成立しない」という形で固定する
// (タスク 3.4 の 2 つ目の箇条書き)。
//
// 復旧の切り詰めだけを行ってジャーナルを消し忘れる実装は、ディスク上の最終状態が
// 正しい実装と一致しうる(この実行のジャーナルで上書きされるため)。それを見分けるため、
// 実装はジャーナルを排他的に作成する契約になっている。すなわち「消し忘れ」は
// 次のジャーナル作成の失敗として必ず露見する。
func TestRecoveredJournalIsDeletedBeforeNextJournalIsWritten(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	const base = "{\"既存\":1}\n"
	writeExisting(t, out, base+`{"途中":`)
	writeExisting(t, journalPathFor(out), wantJournalBytes(int64(len(base))))

	om, err := openOutput(newAppendConfig(out))
	if err != nil {
		t.Fatalf("残存ジャーナルからの復旧で openOutput が失敗した"+
			"(復旧がジャーナルを削除していない可能性がある): %v", err)
	}
	writeRecords(t, om, `{"新":1}`)
	if err := om.Finalize(); err != nil {
		t.Fatalf("Finalize が失敗した: %v", err)
	}

	want := base + `{"新":1}` + "\n"
	if got := readOutput(t, out); string(got) != want {
		t.Errorf("復旧後の内容が期待と異なる\n got=%q\nwant=%q", got, want)
	}
}

// TestCorruptJournalStopsWithExitCodeFour は、解釈できないジャーナルを自動判断せず
// 終了コード 4 で停止することを検証する(タスク 3.4 の 2 つ目の箇条書き、
// design.md「Data Models」:「解釈不能なジャーナル(破損)は終了コード 4 で停止し、
// 手動調査に委ねる」)。
//
// 判定は厳格でなければならない。ジャーナルは「どこまでが利用者のデータか」を決める
// 唯一の手がかりであり、少しでも解釈に迷う値から切り詰め先を推測すると、
// 復旧のつもりでデータを破壊する。停止時は出力ファイルもジャーナルも 1 バイトも
// 変えず、そのまま手動調査に委ねる。
func TestCorruptJournalStopsWithExitCodeFour(t *testing.T) {
	tests := []struct {
		name    string
		journal string
	}{
		{"空(0 バイト。作成直後に強制終了した場合など)", ""},
		{"size= の後に数字がない", "size=\n"},
		{"数字ではない", "size=abc\n"},
		{"負の数(開始前サイズになりえない)", "size=-1\n"},
		{"数字の後に余分な文字がある", "size=12 破損\n"},
		{"複数行", "size=1\nsize=2\n"},
		{"int64 に収まらない巨大な数", "size=99999999999999999999999\n"},
		{"ASCII 範囲外(全角数字)", "size=１２\n"},
		{"末尾の改行がない(書き込みが途中で切れた)", "size=12"},
		{"先頭ゼロ(この実装が書くことのない字句)", "size=007\n"},
		{"size= で始まらない", "12\n"},
		{"前置の空白", " size=12\n"},
		{"接頭辞の綴りが違う", "Size=12\n"},
		{"1 行に収まらない長さ(ジャーナルの体を成さない)", "size=12\n" + strings.Repeat("x", 128)},
		{"数字が桁あふれするほど長い", "size=" + strings.Repeat("9", 64) + "\n"},
	}

	const original = "{\"既存\":\"壊れたジャーナルがあっても 1 バイトも変えてはならない\"}\n"

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			out := filepath.Join(dir, "out.ndjson")
			writeExisting(t, out, original)
			writeExisting(t, journalPathFor(out), tt.journal)

			om, err := openOutput(newAppendConfig(out))
			if om != nil {
				om.Abort()
				t.Fatal("解釈できないジャーナルがあるのに OutputManager が返った" +
					"(自動判断せず停止しなければならない)")
			}
			if code := exitCodeOf(t, err); code != ExitOutput {
				t.Errorf("終了コードが %d。ジャーナル破損は %d でなければならない", code, ExitOutput)
			}
			if !strings.Contains(err.Error(), journalPathFor(out)) {
				t.Errorf("エラーメッセージにジャーナルのパスが含まれない(手動調査に委ねられない): %v", err)
			}
			// 「解釈できないジャーナルとして停止した」ことまで確かめる。
			// ジャーナルは排他的に作成されるため、復旧が破損を見逃して素通りしても
			// 直後のジャーナル作成が EEXIST で失敗し、同じ終了コード 4 になる。
			// この検査がないと、破損の検出を丸ごと外す退行を区別できない。
			if !strings.Contains(err.Error(), "解釈できない") {
				t.Errorf("ジャーナルの解釈失敗として報告されていない"+
					"(破損の検出を外しても排他的作成の失敗で同じ終了コードになるため区別が要る): %v", err)
			}

			// 出力ファイルは 1 バイトも変わってはならない。
			if got := readOutput(t, out); string(got) != original {
				t.Errorf("停止時に出力ファイルが変わった\n got=%q\nwant=%q", got, original)
			}
			// ジャーナルも残さねばならない(手動調査の材料)。
			got, err := os.ReadFile(journalPathFor(out))
			if err != nil {
				t.Fatalf("停止時にジャーナルが失われた: %v", err)
			}
			if string(got) != tt.journal {
				t.Errorf("停止時にジャーナルが書き換わった\n got=%q\nwant=%q", got, tt.journal)
			}
		})
	}
}

// TestUnreadableJournalStopsWithExitCodeFour は、ジャーナルを読めない場合にも
// 停止することを検証する。握り潰して「ジャーナルなし」と扱うと、前回の
// 未確定の追記がそのまま残った上へさらに追記することになる(要件 4.8 の破綻)。
//
// root 実行下では権限による読み取り失敗を作れないため、ジャーナルのパスを
// ディレクトリにして読み取りを失敗させる。
func TestUnreadableJournalStopsWithExitCodeFour(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	const original = "{\"既存\":1}\n"
	writeExisting(t, out, original)
	if err := os.Mkdir(journalPathFor(out), 0o755); err != nil {
		t.Fatalf("読めないジャーナルを準備できない: %v", err)
	}

	om, err := openOutput(newAppendConfig(out))
	if om != nil {
		om.Abort()
		t.Fatal("ジャーナルを読めないのに OutputManager が返った")
	}
	if code := exitCodeOf(t, err); code != ExitOutput {
		t.Errorf("終了コードが %d。%d でなければならない", code, ExitOutput)
	}
	if !strings.Contains(err.Error(), journalPathFor(out)) {
		t.Errorf("エラーメッセージにジャーナルのパスが含まれない: %v", err)
	}
	if got := readOutput(t, out); string(got) != original {
		t.Errorf("停止時に出力ファイルが変わった\n got=%q\nwant=%q", got, original)
	}
}

// TestJournalLargerThanFileStopsWithExitCodeFour は、記録サイズが現在のファイルサイズを
// 上回るジャーナルを解釈不能として扱い、終了コード 4 で停止することを検証する。
//
// 【判断】追記は「開始前サイズ」を記録するのだから、記録サイズがファイルより大きい状態は
// 追記だけでは決して生じない。誰かがファイルを差し替えた・切り詰めた、あるいは
// ジャーナルが別のファイルのものである、といった説明のつかない事態が起きている。
//   - 記録サイズへ「切り詰める」ことはできない(Truncate は伸長になり、NUL の穴を
//     開けて NDJSON を壊す)
//   - ジャーナルを無視して追記を続けるのは、説明のつかない状態の上に書き足すこと
//     であり、要件 4.8 が守ろうとしている「開始前状態」の定義を放棄する
//
// したがって破損ジャーナルと同じ扱い(停止して手動調査)が唯一の安全な選択である。
func TestJournalLargerThanFileStopsWithExitCodeFour(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	const original = "{\"既存\":1}\n"
	writeExisting(t, out, original)
	writeExisting(t, journalPathFor(out), wantJournalBytes(int64(len(original))+1))

	om, err := openOutput(newAppendConfig(out))
	if om != nil {
		om.Abort()
		t.Fatal("記録サイズがファイルより大きいのに OutputManager が返った" +
			"(伸長も無視も要件 4.8 の復旧ではない)")
	}
	if code := exitCodeOf(t, err); code != ExitOutput {
		t.Errorf("終了コードが %d。%d でなければならない", code, ExitOutput)
	}
	if got := readOutput(t, out); string(got) != original {
		t.Errorf("停止時に出力ファイルが変わった(伸長・切り詰めのいずれも行ってはならない)\n got=%q\nwant=%q",
			got, original)
	}
	if got := readOutput(t, journalPathFor(out)); string(got) != wantJournalBytes(int64(len(original))+1) {
		t.Errorf("停止時にジャーナルが書き換わった: %q", got)
	}
}

// TestNewModeRemovesStaleJournalOnCommit は、新規作成モードの置換成功時に stale な
// ジャーナルを削除することを検証する(タスク 3.4 の 2 つ目の箇条書き、
// design.md「output / 復旧」)。
//
// 残したままにすると、次の `--append` 実行が「前回の追記が未確定」と解釈し、
// 置き換えたばかりのファイルを無関係な記録サイズへ切り詰める。すなわち
// 削除の抜けは、次回実行でのデータ消失として現れる。ここではその因果まで通して確認する。
func TestNewModeRemovesStaleJournalOnCommit(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	writeExisting(t, out, strings.Repeat("{\"古い\":\"内容\"}\n", 10))
	// 前回の追記が強制終了された痕跡。サイズは新しい内容と無関係な値にしておく。
	writeExisting(t, journalPathFor(out), wantJournalBytes(3))

	cfg := newOutputConfig(out)
	cfg.Overwrite = true
	om := mustOpenOutput(t, cfg)
	writeRecords(t, om, `{"新しい":"内容"}`)
	if err := om.Finalize(); err != nil {
		t.Fatalf("Finalize が失敗した: %v", err)
	}

	const want = "{\"新しい\":\"内容\"}\n"
	if got := readOutput(t, out); string(got) != want {
		t.Errorf("置換後の内容が期待と異なる\n got=%q\nwant=%q", got, want)
	}
	assertNotExist(t, journalPathFor(out), "置換成功後の stale なジャーナル")

	// 削除されていれば、続く追記実行は切り詰めを行わずに書き足せる。
	om2 := mustOpenOutput(t, newAppendConfig(out))
	writeRecords(t, om2, `{"追記":1}`)
	if err := om2.Finalize(); err != nil {
		t.Fatalf("後続の追記の Finalize が失敗した: %v", err)
	}
	if got, want2 := string(readOutput(t, out)), want+"{\"追記\":1}\n"; got != want2 {
		t.Errorf("stale なジャーナルが後続の追記を壊した\n got=%q\nwant=%q", got, want2)
	}
}

// TestNewModeReportsStaleJournalRemovalFailure は、置換成功後の stale ジャーナル削除に
// 失敗した場合、それを黙って見逃さないことを検証する。
//
// 見逃すと「次回の追記実行が新しいファイルを無関係なサイズへ切り詰める」時限装置を
// 残したまま正常終了することになる。終了コード 4 で報告し、パスを添えて手動対処へ
// 委ねるほうが安全である(design.md「Error Handling」の出力書き込みエラー)。
//
// root 実行下でも失敗させられるよう、ジャーナルのパスを「空でないディレクトリ」に
// しておく(os.Remove は ENOTEMPTY で失敗する)。
func TestNewModeReportsStaleJournalRemovalFailure(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	if err := os.Mkdir(journalPathFor(out), 0o755); err != nil {
		t.Fatalf("削除できないジャーナルを準備できない: %v", err)
	}
	if err := os.WriteFile(filepath.Join(journalPathFor(out), "中身"), []byte("x"), 0o644); err != nil {
		t.Fatalf("削除できないジャーナルを準備できない: %v", err)
	}

	om := mustOpenOutput(t, newOutputConfig(out))
	writeRecords(t, om, `{"a":1}`)

	err := om.Finalize()
	if err == nil {
		t.Fatal("stale なジャーナルを削除できないのに Finalize が成功として報告された")
	}
	if code := exitCodeOf(t, err); code != ExitOutput {
		t.Errorf("終了コードが %d。%d でなければならない", code, ExitOutput)
	}
	if !strings.Contains(err.Error(), journalPathFor(out)) {
		t.Errorf("エラーメッセージにジャーナルのパスが含まれない: %v", err)
	}
}

// TestRecoveryTruncateFailureIsReported は、復旧の切り詰めに失敗した場合に
// 追記を始めず終了コード 4 で停止することを検証する。
//
// ここを握り潰すと、前回の未確定の追記が残ったままのファイルへ、さらに追記を
// 重ねることになる(要件 4.8 の復旧が黙って成立しなくなる)。
//
// 実ファイルの Truncate を失敗させる手立てが root 実行下ではないため、
// パイプの書き込み端を渡す(ftruncate はパイプに対して EINVAL を返す)。
// prepareAppend が末尾読み取り先を引数で受けているのと同じ理由の注入である。
func TestRecoveryTruncateFailureIsReported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("パイプのハンドルに対するロックの意味論が異なるため、この注入は非 Windows 限定")
	}

	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	// 記録サイズ 0 は、パイプの Stat が返すサイズ 0 と一致する。したがって
	// 「記録サイズ > 現在サイズ」の停止条件には掛からず、切り詰めまで到達する。
	writeExisting(t, journalPathFor(out), wantJournalBytes(0))

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("パイプを作成できない: %v", err)
	}
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }()

	om, err := prepareAppend(newAppendConfig(out), w, &countingReaderAt{})
	if om != nil {
		om.Abort()
		t.Fatal("復旧の切り詰めに失敗したのに準備が成功した")
	}
	if code := exitCodeOf(t, err); code != ExitOutput {
		t.Errorf("終了コードが %d。%d でなければならない", code, ExitOutput)
	}
	// メッセージがジャーナルに言及していることまで確かめる。パイプに対しては
	// 後段の Seek も失敗するため、これがないと「復旧で止まった」ことを
	// 「準備の別の段で止まった」ことと区別できない。
	if !strings.Contains(err.Error(), journalPathFor(out)) {
		t.Errorf("復旧の失敗として報告されていない(メッセージにジャーナルのパスがない): %v", err)
	}
	// 復旧に失敗した以上、ジャーナルを消してはならない(手がかりが失われる)。
	if _, err := os.Stat(journalPathFor(out)); err != nil {
		t.Errorf("復旧の失敗時にジャーナルが失われた: %v", err)
	}
}

// TestJournalPathIsBesideOutput は、ジャーナルが出力ファイルと同一ディレクトリに
// 決定的な名前で置かれることを検証する(要件 11.4 / NFR-13、design.md「Data Models」)。
// 一時ディレクトリや作業ディレクトリへ置く実装(要件 11.4 違反)はここで落ちる。
func TestJournalPathIsBesideOutput(t *testing.T) {
	const out = "/var/data/出力 結果.ndjson"
	got := journalPathOf(out)
	if want := journalPathFor(out); got != want {
		t.Errorf("ジャーナルのパスが %q。%q でなければならない", got, want)
	}
	if dir := filepath.Dir(got); dir != filepath.Dir(out) {
		t.Errorf("ジャーナルのディレクトリが %q。出力と同じ %q でなければならない", dir, filepath.Dir(out))
	}
}

// TestJournalOpenFlagsAreSafe は、ジャーナルを作成するフラグの値そのものを見張る。
//
//   - O_EXCL: 復旧が残存ジャーナルを削除し損ねた場合に、ディスク上の最終状態が
//     正しい実装と一致してしまう(この実行のジャーナルで上書きされるため)。
//     排他的作成にしておけば、その退行は作成の失敗として必ず露見する。
//   - O_APPEND なし: Windows で O_APPEND を付けたハンドルは FILE_WRITE_DATA を失う
//     (appendOpenFlags の注記)。ジャーナルを Truncate することはないが、同じ罠を
//     持ち込まない規律として値で固定する。
//   - O_TRUNC なし: O_EXCL と併用しても意味がなく、O_EXCL を外す変更が入ったときに
//     「黙って上書きする」挙動へ滑り落ちる余地を残さない。
func TestJournalOpenFlagsAreSafe(t *testing.T) {
	if journalOpenFlags&os.O_EXCL == 0 {
		t.Error("ジャーナルを排他的に作成していない。復旧の削除漏れが検出できなくなる")
	}
	if journalOpenFlags&os.O_CREATE == 0 {
		t.Error("ジャーナルが O_CREATE で開かれていない。新規に作成できない")
	}
	if journalOpenFlags&os.O_APPEND != 0 {
		t.Error("ジャーナルを O_APPEND で開いている(Windows 固有の罠を持ち込まない規律)")
	}
	if journalOpenFlags&os.O_TRUNC != 0 {
		t.Error("ジャーナルを O_TRUNC で開いている。排他的作成と両立しない")
	}
}

// TestParseJournalSizeAcceptsOnlyCanonicalLine は、ジャーナルの解釈が
// design.md「Data Models」の定める 1 行(`size=<10進数>` + LF)だけを受け付け、
// それ以外を漏れなく拒否することを検証する。
//
// 受理側は上限・下限・境界を、拒否側は「もっともらしいが解釈してはならない」形を
// 押さえる。ここが緩むと、破損したジャーナルから推測した位置でファイルを切り詰める。
func TestParseJournalSizeAcceptsOnlyCanonicalLine(t *testing.T) {
	t.Run("受理", func(t *testing.T) {
		tests := []struct {
			raw  string
			want int64
		}{
			{"size=0\n", 0},
			{"size=1\n", 1},
			{"size=10\n", 10},
			{"size=1234567890\n", 1234567890},
			{"size=9223372036854775807\n", 9223372036854775807}, // int64 の最大値
		}
		for _, tt := range tests {
			t.Run(tt.raw, func(t *testing.T) {
				got, err := parseJournalSize([]byte(tt.raw))
				if err != nil {
					t.Fatalf("正規のジャーナルを拒否した: %v", err)
				}
				if got != tt.want {
					t.Errorf("サイズが %d。%d でなければならない", got, tt.want)
				}
			})
		}
	})

	t.Run("拒否", func(t *testing.T) {
		tests := []struct {
			name string
			raw  string
		}{
			{"空", ""},
			{"改行だけ", "\n"},
			{"接頭辞だけ", "size="},
			{"接頭辞と改行だけ", "size=\n"},
			{"接頭辞がない", "123\n"},
			{"接頭辞の大文字小文字違い", "SIZE=1\n"},
			{"末尾の改行がない", "size=1"},
			{"CRLF 終端(ASCII 1 行の規定は LF)", "size=1\r\n"},
			{"改行が 2 個", "size=1\n\n"},
			{"複数行", "size=1\nsize=2\n"},
			{"前置の空白", " size=1\n"},
			{"後置の空白", "size=1 \n"},
			{"数字の後に文字", "size=1x\n"},
			{"正符号", "size=+1\n"},
			{"負符号", "size=-1\n"},
			{"小数", "size=1.5\n"},
			{"16 進表記", "size=0x10\n"},
			{"先頭ゼロ", "size=01\n"},
			{"先頭ゼロ(ゼロだけは受理される)", "size=00\n"},
			{"int64 の最大値+1", "size=9223372036854775808\n"},
			{"桁あふれ", "size=" + strings.Repeat("9", 30) + "\n"},
			{"全角数字", "size=１\n"},
			{"NUL 混入", "size=1\x00\n"},
			{"長すぎる", "size=1\n" + strings.Repeat("x", journalMaxBytes)},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got, err := parseJournalSize([]byte(tt.raw))
				if err == nil {
					t.Fatalf("解釈してはならないジャーナル %q をサイズ %d として受理した", tt.raw, got)
				}
			})
		}
	})
}

// TestSyncContainingDir は、ジャーナル作成後のディレクトリ同期が実在のディレクトリに
// 対して呼べること、および存在しないディレクトリでも呼び出し側を巻き込まないことを
// 検証する(要件 4.8 の永続性の上積み)。
//
// 【限界】同期が実際に行われたかはファイルシステム API からは観測できない。
// この関数の呼び出しが writeJournal から外される変異は、どのテストでも検出できない
// (tasks.md の未解決所見に記録した Sync 一般の限界と同じ性質)。
func TestSyncContainingDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.ndjson.journal")
	if err := os.WriteFile(path, []byte(wantJournalBytes(0)), 0o644); err != nil {
		t.Fatalf("ジャーナルを準備できない: %v", err)
	}
	// 失敗しても報告しない契約(Windows のディレクトリハンドルは Sync できない)。
	// panic せず戻ることと、対象を壊さないことを確認する。
	syncContainingDir(path)
	syncContainingDir(filepath.Join(dir, "存在しない", "out.ndjson.journal"))

	if got := readOutput(t, path); string(got) != wantJournalBytes(0) {
		t.Errorf("ディレクトリ同期でジャーナルが変わった: %q", got)
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
