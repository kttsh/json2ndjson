package main

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// testSink は RecordSink のテスト実装(タスク 2.3 の完了条件「テスト用シンク」)。
//
// WriteRecord が受け取るスライスは呼び出しの間しか有効でない。convert はレコードの
// バッファを再利用するため、保持したいなら写しを取らなければならない。本物の実装
// (output)は受け取った時点で bufio.Writer へ書き出すのでこの制約で足りる。
type testSink struct {
	records [][]byte
	// failAt が n>0 のとき、n 件目の WriteRecord で failErr を返す。
	failAt  int
	failErr error
}

func (s *testSink) WriteRecord(compact jsontext.Value) error {
	if s.failAt > 0 && len(s.records)+1 == s.failAt {
		return s.failErr
	}
	s.records = append(s.records, bytes.Clone(compact))
	return nil
}

// lines は受け取ったレコードを文字列として返す(バイト単位の比較用)。
func (s *testSink) lines() []string {
	out := make([]string, 0, len(s.records))
	for _, r := range s.records {
		out = append(out, string(r))
	}
	return out
}

// writeInput は入力 JSON ファイルを作って絶対パスを返す。
func writeInput(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("入力ファイル %s を作成できません: %v", p, err)
	}
	return p
}

// convertConfig は変換に必要な最小限の Config を組み立てる。
func convertConfig(t *testing.T, path string, inputs ...string) *Config {
	t.Helper()
	return &Config{
		Inputs:          inputs,
		Out:             "out.ndjson",
		Path:            *mustPointer(t, path),
		Newline:         "\n",
		TrailingNewline: true,
	}
}

// runConvert は runConversion をテスト用シンクで実行する。
func runConvert(t *testing.T, cfg *Config) (*ConvertResult, *testSink, error) {
	t.Helper()
	sink := &testSink{}
	res, err := runConversion(cfg, sink)
	return res, sink, err
}

// assertLines はシンクが受け取ったレコードをバイト単位で照合する。
func assertLines(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("レコード数が %d 件です(期待 %d 件): %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d 件目のレコードが %q です(期待 %q)", i+1, got[i], want[i])
		}
	}
}

// --- (1) 構造の判定: 素の配列・エンベロープ・単一オブジェクト・空配列 -------------
// 要件 2.2、2.3、3.10 / タスク 2.3 の完了条件

func TestRunConversionStructures(t *testing.T) {
	tests := []struct {
		name  string
		input string
		path  string
		want  []string
	}{
		{
			name:  "素の配列は各要素が 1 レコードになる",
			input: `[{"id":1},{"id":2},{"id":3}]`,
			path:  "",
			want:  []string{`{"id":1}`, `{"id":2}`, `{"id":3}`},
		},
		{
			name:  "エンベロープ付きは --path で配列へ降りる",
			input: `{"meta":{"page":1},"data":[{"id":1},{"id":2}],"next":null}`,
			path:  "/data",
			want:  []string{`{"id":1}`, `{"id":2}`},
		},
		{
			name:  "指定位置がオブジェクトなら全体が 1 レコードになる",
			input: `{"envelope":{"id":7,"name":"x"}}`,
			path:  "/envelope",
			want:  []string{`{"id":7,"name":"x"}`},
		},
		{
			name:  "ルートが単一オブジェクトでも 1 レコードになる",
			input: `{"id":7}`,
			path:  "",
			want:  []string{`{"id":7}`},
		},
		{
			name:  "空配列はレコードを 1 件も出力せず正常終了する",
			input: `{"data":[]}`,
			path:  "/data",
			want:  nil,
		},
		{
			name:  "ルートの空配列もレコードを出力しない",
			input: `[]`,
			path:  "",
			want:  nil,
		},
		{
			name:  "空オブジェクトは 1 レコードとして出力する",
			input: `{"data":{}}`,
			path:  "/data",
			want:  []string{`{}`},
		},
		{
			name:  "配列添字の参照トークンで配列の中へ降りられる",
			input: `{"pages":[{"data":[{"id":1}]},{"data":[{"id":2}]}]}`,
			path:  "/pages/1/data",
			want:  []string{`{"id":2}`},
		},
		{
			name:  "エスケープを含むキーへ降りられる",
			input: `{"a/b":[{"id":1}]}`,
			path:  "/a~1b",
			want:  []string{`{"id":1}`},
		},
		{
			name:  "空文字列キーはルートと区別される",
			input: `{"":[{"id":1}]}`,
			path:  "/",
			want:  []string{`{"id":1}`},
		},
		{
			name:  "配列要素がスカラーでもそのまま 1 レコードになる",
			input: `[1,"x",true,null]`,
			path:  "",
			want:  []string{`1`, `"x"`, `true`, `null`},
		},
		{
			name:  "入れ子の配列要素も 1 レコードになる",
			input: `{"data":[[1,2],[3]]}`,
			path:  "/data",
			want:  []string{`[1,2]`, `[3]`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := writeInput(t, "in.json", tt.input)
			res, sink, err := runConvert(t, convertConfig(t, tt.path, in))
			if err != nil {
				t.Fatalf("変換が失敗しました: %v", err)
			}
			assertLines(t, sink.lines(), tt.want)
			if res.Records != int64(len(tt.want)) {
				t.Errorf("Records が %d です(期待 %d)", res.Records, len(tt.want))
			}
			if res.Files != 1 {
				t.Errorf("Files が %d です(期待 1)", res.Files)
			}
			if res.LastValue != nil {
				t.Errorf("--cursor-key 未指定なのに LastValue が %q です(期待 nil)", res.LastValue)
			}
		})
	}
}

// --- (2) 位置不正 = 終了コード 5(要件 2.4)---------------------------------------

func TestRunConversionInvalidLocation(t *testing.T) {
	tests := []struct {
		name  string
		input string
		path  string
	}{
		{name: "指定位置が文字列", input: `{"data":"x"}`, path: "/data"},
		{name: "指定位置が数値", input: `{"data":1}`, path: "/data"},
		{name: "指定位置が真偽値", input: `{"data":true}`, path: "/data"},
		{name: "指定位置が null", input: `{"data":null}`, path: "/data"},
		{name: "ルートがスカラー", input: `123`, path: ""},
		{name: "ルートが文字列", input: `"abc"`, path: ""},
		{name: "指定したキーが存在しない", input: `{"data":[]}`, path: "/missing"},
		{name: "配列の添字が範囲外", input: `{"data":[{"id":1}]}`, path: "/data/5"},
		{name: "配列に対する参照トークンが添字でない", input: `{"data":[{"id":1}]}`, path: "/data/01"},
		{name: "配列末尾を指す - は添字ではない", input: `{"data":[{"id":1}]}`, path: "/data/-"},
		{name: "スカラーの内側へは降りられない", input: `{"data":{"x":1}}`, path: "/data/x/y"},
		{name: "空配列の要素は指せない", input: `{"data":[]}`, path: "/data/0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := writeInput(t, "in.json", tt.input)
			res, sink, err := runConvert(t, convertConfig(t, tt.path, in))
			if got := exitCodeOf(t, err); got != ExitLocation {
				t.Fatalf("終了コードが %d です(期待 %d): %v", got, ExitLocation, err)
			}
			if res != nil {
				t.Errorf("エラー時に結果 %+v が返されました(期待 nil)", res)
			}
			if len(sink.records) != 0 {
				t.Errorf("エラー時に %d 件が書き込まれました(期待 0 件)", len(sink.records))
			}
			// 呼び出し元が原因を切り分けられるよう、メッセージにファイル名と
			// JSON Pointer を含める。
			msg := err.Error()
			if !strings.Contains(msg, filepath.Base(in)) {
				t.Errorf("エラーメッセージにファイル名が含まれていません: %s", msg)
			}
			if tt.path != "" && !strings.Contains(msg, tt.path) {
				t.Errorf("エラーメッセージに JSON Pointer %q が含まれていません: %s", tt.path, msg)
			}
		})
	}
}

// TestRunConversionDoesNotInterpretErrorPayloads は、エラー応答らしき内容でも
// 自動判定せず通常のレコードとして変換することを確かめる(要件 2.5 / FR-09)。
// HTTP ステータスによるエラー判定は呼び出し元の責務である。
func TestRunConversionDoesNotInterpretErrorPayloads(t *testing.T) {
	tests := []struct {
		name  string
		input string
		path  string
		want  []string
	}{
		{
			name:  "エラーオブジェクトも 1 レコードとして出力する",
			input: `{"error":{"code":401,"message":"unauthorized"}}`,
			path:  "",
			want:  []string{`{"error":{"code":401,"message":"unauthorized"}}`},
		},
		{
			name:  "配列内のエラー要素もそのまま出力する",
			input: `{"data":[{"error":"rate limited"},{"id":1}]}`,
			path:  "/data",
			want:  []string{`{"error":"rate limited"}`, `{"id":1}`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := writeInput(t, "in.json", tt.input)
			res, sink, err := runConvert(t, convertConfig(t, tt.path, in))
			if err != nil {
				t.Fatalf("変換が失敗しました: %v", err)
			}
			assertLines(t, sink.lines(), tt.want)
			if res.Records != int64(len(tt.want)) {
				t.Errorf("Records が %d です(期待 %d)", res.Records, len(tt.want))
			}
		})
	}
}

// --- (3) BOM の読み飛ばし(要件 1.1 / FR-01)--------------------------------------

func TestRunConversionSkipsUTF8BOM(t *testing.T) {
	const body = `{"data":[{"id":1},{"id":2}]}`
	const bom = "\xef\xbb\xbf"

	plain := writeInput(t, "plain.json", body)
	withBOM := writeInput(t, "bom.json", bom+body)

	_, plainSink, err := runConvert(t, convertConfig(t, "/data", plain))
	if err != nil {
		t.Fatalf("BOM なしの変換が失敗しました: %v", err)
	}
	res, bomSink, err := runConvert(t, convertConfig(t, "/data", withBOM))
	if err != nil {
		t.Fatalf("BOM 付きの変換が失敗しました: %v", err)
	}

	assertLines(t, bomSink.lines(), plainSink.lines())
	assertLines(t, bomSink.lines(), []string{`{"id":1}`, `{"id":2}`})
	if res.Records != 2 {
		t.Errorf("Records が %d です(期待 2)", res.Records)
	}
}

func TestRunConversionBOMOnlyAtFileStart(t *testing.T) {
	// レコードの内側に現れる U+FEFF は文字列の一部であり、読み飛ばしてはならない。
	record := "{\"a\":\"\ufeffx\"}"
	in := writeInput(t, "in.json", "["+record+"]")
	_, sink, err := runConvert(t, convertConfig(t, "", in))
	if err != nil {
		t.Fatalf("変換が失敗しました: %v", err)
	}
	assertLines(t, sink.lines(), []string{record})
}

// --- (4) 複数入力ファイルの順次処理(要件 1.2 / FR-02)----------------------------

func TestRunConversionMultipleFilesInGivenOrder(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("入力ファイル %s を作成できません: %v", p, err)
		}
		return p
	}
	a := write("a.json", `[{"id":1},{"id":2}]`)
	b := write("b.json", `[{"id":3}]`)
	c := write("c.json", `[]`)

	t.Run("与えられた順に処理する", func(t *testing.T) {
		res, sink, err := runConvert(t, convertConfig(t, "", a, b, c))
		if err != nil {
			t.Fatalf("変換が失敗しました: %v", err)
		}
		assertLines(t, sink.lines(), []string{`{"id":1}`, `{"id":2}`, `{"id":3}`})
		if res.Files != 3 {
			t.Errorf("Files が %d です(期待 3)", res.Files)
		}
		if res.Records != 3 {
			t.Errorf("Records が %d です(期待 3)", res.Records)
		}
	})

	t.Run("整列は cli の責務であり convert は並べ替えない", func(t *testing.T) {
		// 昇順(a, b)ではなく b, a の順で渡す。convert が再整列すると
		// レコードの順序が 1,2,3 になってしまう。
		_, sink, err := runConvert(t, convertConfig(t, "", b, a))
		if err != nil {
			t.Fatalf("変換が失敗しました: %v", err)
		}
		assertLines(t, sink.lines(), []string{`{"id":3}`, `{"id":1}`, `{"id":2}`})
	})
}

// --- (5) 入力ファイル不可 = 終了コード 2(要件 1.5)-------------------------------

func TestRunConversionInputErrors(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.json")
	ok := filepath.Join(dir, "ok.json")
	if err := os.WriteFile(ok, []byte(`[{"id":1}]`), 0o600); err != nil {
		t.Fatalf("入力ファイルを作成できません: %v", err)
	}
	subdir := filepath.Join(dir, "adir")
	if err := os.Mkdir(subdir, 0o700); err != nil {
		t.Fatalf("ディレクトリを作成できません: %v", err)
	}

	tests := []struct {
		name   string
		inputs []string
	}{
		{name: "入力ファイルが存在しない", inputs: []string{missing}},
		{name: "入力がディレクトリで読めない", inputs: []string{subdir}},
		{name: "2 つ目の入力が存在しない", inputs: []string{ok, missing}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, _, err := runConvert(t, convertConfig(t, "", tt.inputs...))
			if got := exitCodeOf(t, err); got != ExitInput {
				t.Fatalf("終了コードが %d です(期待 %d): %v", got, ExitInput, err)
			}
			if res != nil {
				t.Errorf("エラー時に結果 %+v が返されました(期待 nil)", res)
			}
			if !strings.Contains(err.Error(), filepath.Base(tt.inputs[len(tt.inputs)-1])) {
				t.Errorf("エラーメッセージにファイル名が含まれていません: %v", err)
			}
		})
	}
}

// --- (6) 構文不正 = 終了コード 3 + ファイル名・バイトオフセット・JSON Pointer ----
// 要件 1.6 / FR-38

var (
	byteOffsetPattern = regexp.MustCompile(`バイトオフセット (\d+)`)
	jsonPointerPatter = regexp.MustCompile(`JSON Pointer "([^"]*)"`)
)

func TestRunConversionSyntaxError(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		path        string
		wantOffset  bool   // 0 より大きいバイトオフセットを期待するか
		wantPointer string // 期待する JSON Pointer(前方一致)。"" は検査しない
	}{
		{
			name:        "配列要素の途中で値が欠けている",
			input:       `{"data":[{"id":1},{"id":}]}`,
			path:        "/data",
			wantOffset:  true,
			wantPointer: "/data/1",
		},
		{
			name:        "レコードの閉じ括弧がない",
			input:       `{"data":[{"id":1}`,
			path:        "/data",
			wantOffset:  true,
			wantPointer: "/data",
		},
		{
			name:       "空ファイルは JSON ではない",
			input:      ``,
			path:       "",
			wantOffset: false,
		},
		{
			name:       "先頭から不正なトークン",
			input:      `not json`,
			path:       "",
			wantOffset: false,
		},
		{
			name:       "移動途中のオブジェクトが壊れている",
			input:      `{"data" [1]}`,
			path:       "/data",
			wantOffset: true,
		},
		{
			name:       "末尾にごみが付いている",
			input:      `[{"id":1}] ごみ`,
			path:       "",
			wantOffset: true,
		},
		{
			name:       "対象の後ろにある兄弟の値が壊れている",
			input:      `{"data":[{"id":1}],"meta":{]}`,
			path:       "/data",
			wantOffset: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := writeInput(t, "broken.json", tt.input)
			res, _, err := runConvert(t, convertConfig(t, tt.path, in))
			if got := exitCodeOf(t, err); got != ExitSyntax {
				t.Fatalf("終了コードが %d です(期待 %d): %v", got, ExitSyntax, err)
			}
			if res != nil {
				t.Errorf("エラー時に結果 %+v が返されました(期待 nil)", res)
			}

			msg := err.Error()
			if !strings.Contains(msg, filepath.Base(in)) {
				t.Errorf("エラーメッセージにファイル名が含まれていません: %s", msg)
			}
			m := byteOffsetPattern.FindStringSubmatch(msg)
			if m == nil {
				t.Fatalf("エラーメッセージにバイトオフセットが含まれていません: %s", msg)
			}
			if tt.wantOffset && m[1] == "0" {
				t.Errorf("バイトオフセットが 0 です(0 より大きい値を期待): %s", msg)
			}
			p := jsonPointerPatter.FindStringSubmatch(msg)
			if p == nil {
				t.Fatalf("エラーメッセージに JSON Pointer が含まれていません: %s", msg)
			}
			if tt.wantPointer != "" && !strings.HasPrefix(p[1], tt.wantPointer) {
				t.Errorf("JSON Pointer が %q です(%q で始まることを期待): %s", p[1], tt.wantPointer, msg)
			}
		})
	}
}

// TestRunConversionSyntaxErrorOffsetIncludesBOM は、報告するバイトオフセットが
// ファイル先頭からの絶対位置(= BOM を含む)であることを確かめる。
func TestRunConversionSyntaxErrorOffsetIncludesBOM(t *testing.T) {
	const broken = `{"data":[{"id":1},{"id":}]}`
	const bom = "\xef\xbb\xbf"

	offsetOf := func(content string) string {
		t.Helper()
		in := writeInput(t, "broken.json", content)
		_, _, err := runConvert(t, convertConfig(t, "/data", in))
		if got := exitCodeOf(t, err); got != ExitSyntax {
			t.Fatalf("終了コードが %d です(期待 %d): %v", got, ExitSyntax, err)
		}
		m := byteOffsetPattern.FindStringSubmatch(err.Error())
		if m == nil {
			t.Fatalf("エラーメッセージにバイトオフセットが含まれていません: %v", err)
		}
		return m[1]
	}

	plain, err := strconv.Atoi(offsetOf(broken))
	if err != nil {
		t.Fatalf("バイトオフセットを解釈できません: %v", err)
	}
	if plain <= 0 {
		t.Fatalf("BOM なしのバイトオフセットが %d です(0 より大きい値を期待)", plain)
	}
	withBOM, err := strconv.Atoi(offsetOf(bom + broken))
	if err != nil {
		t.Fatalf("バイトオフセットを解釈できません: %v", err)
	}
	if withBOM != plain+len(bom) {
		t.Errorf("BOM 付きのバイトオフセットが %d です(BOM の %d バイトを加えた %d を期待)",
			withBOM, len(bom), plain+len(bom))
	}
}

// --- (7) レコード内部のネスト深さ上限 512(要件 10.2、タスク 2.4 の境界)---------

func nestedArrays(depth int) string {
	return strings.Repeat("[", depth) + "1" + strings.Repeat("]", depth)
}

func TestRunConversionNestingDepthBoundary(t *testing.T) {
	tests := []struct {
		name    string
		depth   int
		wantErr bool
	}{
		{name: "深さ 511 は許可", depth: 511, wantErr: false},
		{name: "深さ 512 は許可(上限そのもの)", depth: 512, wantErr: false},
		{name: "深さ 513 は構文エラー", depth: 513, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := nestedArrays(tt.depth)
			in := writeInput(t, "deep.json", "["+record+"]")
			_, sink, err := runConvert(t, convertConfig(t, "", in))
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("深さ %d が拒否されました: %v", tt.depth, err)
				}
				assertLines(t, sink.lines(), []string{record})
				return
			}
			if got := exitCodeOf(t, err); got != ExitSyntax {
				t.Fatalf("終了コードが %d です(期待 %d): %v", got, ExitSyntax, err)
			}
			msg := err.Error()
			if !strings.Contains(msg, filepath.Base(in)) {
				t.Errorf("エラーメッセージにファイル名が含まれていません: %s", msg)
			}
			if !strings.Contains(msg, "512") {
				t.Errorf("エラーメッセージに上限値 512 が含まれていません: %s", msg)
			}
			if len(sink.records) != 0 {
				t.Errorf("深さ超過なのに %d 件が書き込まれました", len(sink.records))
			}
		})
	}
}

func TestRunConversionNestingDepthCountsObjects(t *testing.T) {
	// オブジェクトのネストも同じ上限で数える。
	deep := strings.Repeat(`{"a":`, 513) + `1` + strings.Repeat(`}`, 513)
	in := writeInput(t, "deep.json", "["+deep+"]")
	_, _, err := runConvert(t, convertConfig(t, "", in))
	if got := exitCodeOf(t, err); got != ExitSyntax {
		t.Fatalf("終了コードが %d です(期待 %d): %v", got, ExitSyntax, err)
	}
}

func TestExceedsNestingDepth(t *testing.T) {
	tests := []struct {
		name  string
		value string
		limit int
		want  bool
	}{
		{name: "スカラーは深さ 0", value: `1`, limit: 0, want: false},
		{name: "空オブジェクトは深さ 1", value: `{}`, limit: 1, want: false},
		{name: "空オブジェクトは上限 0 を超える", value: `{}`, limit: 0, want: true},
		{name: "2 段の入れ子", value: `[[1]]`, limit: 2, want: false},
		{name: "2 段の入れ子は上限 1 を超える", value: `[[1]]`, limit: 1, want: true},
		{name: "兄弟の深さは合算しない", value: `[[1],[2],[3]]`, limit: 2, want: false},
		{name: "文字列の中の括弧は数えない", value: `{"a":"{{{{{{"}`, limit: 1, want: false},
		{name: "文字列の中の閉じ括弧も数えない", value: `{"a":"}}}}","b":[1]}`, limit: 2, want: false},
		{name: "エスケープされた引用符で文字列は終わらない", value: `{"a":"\"[[[[","b":1}`, limit: 1, want: false},
		{name: "エスケープされた円記号の直後の引用符は文字列を閉じる", value: `["\\",[1]]`, limit: 2, want: false},
		{name: "最深部だけが判定対象", value: `[1,[2,[3,[4]]]]`, limit: 3, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exceedsNestingDepth(jsontext.Value(tt.value), tt.limit); got != tt.want {
				t.Errorf("exceedsNestingDepth(%s, %d) = %v(期待 %v)", tt.value, tt.limit, got, tt.want)
			}
		})
	}
}

// --- (8) 字句保持(要件 3.7、3.8、3.9)-------------------------------------------

func TestRunConversionPreservesLexemes(t *testing.T) {
	tests := []struct {
		name   string
		record string
	}{
		{name: "1.0 を 1 にしない", record: `{"n":1.0}`},
		{name: "指数表記をそのまま保つ", record: `{"n":1e10}`},
		{name: "大文字の指数表記も保つ", record: `{"n":-1.5E+300}`},
		{name: "64 ビットを超える整数を浮動小数点にしない", record: `{"n":123456789012345678901234567890}`},
		{name: "小数点以下 20 桁を丸めない", record: `{"n":0.12345678901234567890}`},
		{name: "先頭のゼロ付き小数を保つ", record: `{"n":0.0}`},
		{name: "\\uXXXX を展開しない", record: `{"s":"\u00e9"}`},
		{name: "サロゲートペアのエスケープを展開しない", record: `{"s":"\ud83d\ude00"}`},
		{name: "生の絵文字を \\uXXXX 化しない", record: `{"s":"😀"}`},
		{name: "日本語をそのまま保つ", record: `{"s":"日本語のキーと値"}`},
		{name: "エスケープされた制御文字を保つ", record: `{"s":"a\nb\tc\u0000d"}`},
		{name: "スラッシュのエスケープを保つ", record: `{"s":"a\/b"}`},
		{name: "重複キーをそのまま通す", record: `{"a":1,"a":2}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := writeInput(t, "in.json", "["+tt.record+"]")
			_, sink, err := runConvert(t, convertConfig(t, "", in))
			if err != nil {
				t.Fatalf("変換が失敗しました: %v", err)
			}
			assertLines(t, sink.lines(), []string{tt.record})
		})
	}
}

// --- (9) コンパクト化とネスト保持(要件 3.1、3.6)--------------------------------

func TestRunConversionCompactsWithoutFlattening(t *testing.T) {
	input := `{
	  "data" : [
	    {
	      "id" : 1 ,
	      "nested" : { "a" : [ 1 , 2 , { "b" : null } ] }
	    } ,
	    {  "id"  :  2  }
	  ]
	}`
	in := writeInput(t, "pretty.json", input)
	_, sink, err := runConvert(t, convertConfig(t, "/data", in))
	if err != nil {
		t.Fatalf("変換が失敗しました: %v", err)
	}
	assertLines(t, sink.lines(), []string{
		`{"id":1,"nested":{"a":[1,2,{"b":null}]}}`,
		`{"id":2}`,
	})
	for i, r := range sink.records {
		if bytes.ContainsAny(r, " \t\n\r") {
			t.Errorf("%d 件目のレコードに要素間の空白が残っています: %q", i+1, r)
		}
	}
}

// --- (10) 制御文字のエスケープ維持(要件 3.2)------------------------------------

func TestRunConversionKeepsControlCharactersEscaped(t *testing.T) {
	in := writeInput(t, "ctrl.json", `[{"s":"1 行目\n2 行目\r\tタブ\u0007ベル"}]`)
	_, sink, err := runConvert(t, convertConfig(t, "", in))
	if err != nil {
		t.Fatalf("変換が失敗しました: %v", err)
	}
	assertLines(t, sink.lines(), []string{`{"s":"1 行目\n2 行目\r\tタブ\u0007ベル"}`})
	for i, r := range sink.records {
		for _, b := range r {
			if b < 0x20 {
				t.Fatalf("%d 件目のレコードに生の制御文字 0x%02x が含まれています: %q", i+1, b, r)
			}
		}
	}
}

// --- (11) LF と CRLF の混在許容(要件 1.3 / FR-03)-------------------------------

func TestRunConversionAcceptsMixedLineEndings(t *testing.T) {
	input := "{\r\n\"data\": [\n{\"id\": 1},\r\n{\"id\": 2}\n]\r\n}"
	in := writeInput(t, "crlf.json", input)
	_, sink, err := runConvert(t, convertConfig(t, "/data", in))
	if err != nil {
		t.Fatalf("変換が失敗しました: %v", err)
	}
	assertLines(t, sink.lines(), []string{`{"id":1}`, `{"id":2}`})
}

// --- (12) 入力ファイルを変更しない(要件 1.4 / FR-04)----------------------------

func TestRunConversionDoesNotModifyInput(t *testing.T) {
	const content = `{
	  "data" : [ {"id":1}, {"id":2} ]
	}`
	in := writeInput(t, "in.json", content)

	// mtime の粒度が粗い環境でも差が出るよう、過去の時刻に固定してから実行する。
	past := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(in, past, past); err != nil {
		t.Fatalf("mtime を設定できません: %v", err)
	}
	before, err := os.Stat(in)
	if err != nil {
		t.Fatalf("stat できません: %v", err)
	}

	if _, _, err := runConvert(t, convertConfig(t, "/data", in)); err != nil {
		t.Fatalf("変換が失敗しました: %v", err)
	}

	after, err := os.Stat(in)
	if err != nil {
		t.Fatalf("stat できません: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("入力ファイルの更新時刻が %v から %v へ変わりました", before.ModTime(), after.ModTime())
	}
	if after.Size() != before.Size() {
		t.Errorf("入力ファイルのサイズが %d から %d へ変わりました", before.Size(), after.Size())
	}
	got, err := os.ReadFile(in)
	if err != nil {
		t.Fatalf("読み直せません: %v", err)
	}
	if string(got) != content {
		t.Errorf("入力ファイルの内容が変わりました:\n実際: %q\n期待: %q", got, content)
	}
}

// --- (13) LastValue: 最後に「出力した」レコードの写しを保持する ------------------
// design.md「convert / Service Interface」、tasks.md の未配線契約

func TestRunConversionLastValueIsNilWithoutCursorKey(t *testing.T) {
	in := writeInput(t, "in.json", `[{"id":1},{"id":2}]`)
	res, _, err := runConvert(t, convertConfig(t, "", in))
	if err != nil {
		t.Fatalf("変換が失敗しました: %v", err)
	}
	if res.LastValue != nil {
		t.Errorf("LastValue が %q です(--cursor-key 未指定なので nil を期待)", res.LastValue)
	}
}

func TestRunConversionLastValueIsNilWhenNoRecords(t *testing.T) {
	in := writeInput(t, "in.json", `{"data":[]}`)
	cfg := convertConfig(t, "/data", in)
	cfg.CursorKey = mustPointer(t, "/id")
	res, _, err := runConvert(t, cfg)
	if err != nil {
		t.Fatalf("変換が失敗しました: %v", err)
	}
	if res.Records != 0 {
		t.Fatalf("Records が %d です(期待 0)", res.Records)
	}
	if res.LastValue != nil {
		t.Errorf("LastValue が %q です(0 件なので nil を期待)", res.LastValue)
	}
}

func TestRunConversionLastValueIsLastEmittedRecord(t *testing.T) {
	in := writeInput(t, "in.json", `[{"id":1},{"id":2},{"id":3}]`)
	cfg := convertConfig(t, "", in)
	cfg.CursorKey = mustPointer(t, "/id")
	res, _, err := runConvert(t, cfg)
	if err != nil {
		t.Fatalf("変換が失敗しました: %v", err)
	}
	if string(res.LastValue) != `{"id":3}` {
		t.Errorf("LastValue が %q です(期待 %q)", res.LastValue, `{"id":3}`)
	}
}

// TestRunConversionLastValueSurvivesLaterRecords は tasks.md が警告する別名参照
// (Apply が素通し経路で入力スライスそのものを返す)を検出する。
//
// 最後の配列要素が重複排除で「読まれたが出力されなかった」場合、LastValue が
// レコードバッファを指したままだと、後から読まれたレコードの内容で上書きされる。
// 上書きを確実に観測できるよう、後続レコードは先行レコードより短くしてある
// (append が再確保するとバッファが入れ替わり、別名参照でも偶然一致してしまうため)。
func TestRunConversionLastValueSurvivesLaterRecords(t *testing.T) {
	const emitted = `{"id":2,"name":"beta-record"}`
	const dropped = `{"id":1,"name":"x"}`
	if len(dropped) >= len(emitted) {
		t.Fatalf("テストの前提が壊れています: 後続レコードは先行レコードより短くなければなりません")
	}

	in := writeInput(t, "in.json", `[{"id":1,"name":"alpha"},`+emitted+`,`+dropped+`]`)
	cfg := convertConfig(t, "", in)
	cfg.CursorKey = mustPointer(t, "/id")
	cfg.DedupeKey = mustPointer(t, "/id")

	res, sink, err := runConvert(t, cfg)
	if err != nil {
		t.Fatalf("変換が失敗しました: %v", err)
	}
	assertLines(t, sink.lines(), []string{`{"id":1,"name":"alpha"}`, emitted})
	if res.Records != 2 {
		t.Errorf("Records が %d です(重複排除後の 2 を期待)", res.Records)
	}
	if string(res.LastValue) != emitted {
		t.Errorf("LastValue が %q です(最後に出力したレコード %q を期待)", res.LastValue, emitted)
	}
}

func TestRunConversionLastValueSurvivesSkippedOversizeRecord(t *testing.T) {
	const emitted = `{"id":2,"name":"beta-record"}`
	const oversize = `{"id":3,"name":"very-long-value-that-exceeds-the-limit"}`

	in := writeInput(t, "in.json", `[`+emitted+`,`+oversize+`]`)
	cfg := convertConfig(t, "", in)
	cfg.CursorKey = mustPointer(t, "/id")
	cfg.MaxRecordBytes = int64(len(emitted))
	cfg.SkipOversize = true

	res, sink, err := runConvert(t, cfg)
	if err != nil {
		t.Fatalf("変換が失敗しました: %v", err)
	}
	assertLines(t, sink.lines(), []string{emitted})
	if res.Records != 1 {
		t.Errorf("Records が %d です(スキップ除外後の 1 を期待)", res.Records)
	}
	if string(res.LastValue) != emitted {
		t.Errorf("LastValue が %q です(期待 %q)", res.LastValue, emitted)
	}
}

// TestRunConversionLastValueSurvivesLaterFiles は、後続の入力ファイルのレコードが
// すべて重複排除で落ちても LastValue が壊れないことを確かめる。
//
// レコードバッファはファイルをまたいで再利用されるため、LastValue が別名参照だと
// 2 つ目のファイルの読み取りで内容が上書きされる。上書きを観測できるよう、
// 2 つ目のファイルのレコードは 1 つ目より短くしてある。
func TestRunConversionLastValueSurvivesLaterFiles(t *testing.T) {
	const emitted = `{"id":2,"name":"beta-record"}`
	const dropped = `{"id":2,"name":"x"}`
	if len(dropped) >= len(emitted) {
		t.Fatalf("テストの前提が壊れています: 後続レコードは先行レコードより短くなければなりません")
	}

	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("入力ファイル %s を作成できません: %v", p, err)
		}
		return p
	}
	a := write("a.json", `[`+emitted+`]`)
	b := write("b.json", `[`+dropped+`]`)

	cfg := convertConfig(t, "", a, b)
	cfg.CursorKey = mustPointer(t, "/id")
	cfg.DedupeKey = mustPointer(t, "/id")

	res, sink, err := runConvert(t, cfg)
	if err != nil {
		t.Fatalf("変換が失敗しました: %v", err)
	}
	assertLines(t, sink.lines(), []string{emitted})
	if res.Files != 2 {
		t.Errorf("Files が %d です(期待 2)", res.Files)
	}
	if string(res.LastValue) != emitted {
		t.Errorf("LastValue が %q です(最後に出力したレコード %q を期待)", res.LastValue, emitted)
	}
}

// --- (14) Records は重複排除・スキップ除外後の出力件数(tasks.md タスク 5.1)-----

func TestRunConversionRecordsExcludeDroppedRecords(t *testing.T) {
	t.Run("重複排除で落ちたレコードは数えない", func(t *testing.T) {
		in := writeInput(t, "in.json", `[{"id":1},{"id":2},{"id":1},{"id":2},{"id":3}]`)
		cfg := convertConfig(t, "", in)
		cfg.DedupeKey = mustPointer(t, "/id")
		res, sink, err := runConvert(t, cfg)
		if err != nil {
			t.Fatalf("変換が失敗しました: %v", err)
		}
		assertLines(t, sink.lines(), []string{`{"id":1}`, `{"id":2}`, `{"id":3}`})
		if res.Records != 3 {
			t.Errorf("Records が %d です(期待 3)", res.Records)
		}
	})

	t.Run("サイズ超過でスキップしたレコードは数えない", func(t *testing.T) {
		in := writeInput(t, "in.json", `[{"id":1},{"id":1234567890},{"id":2}]`)
		cfg := convertConfig(t, "", in)
		cfg.MaxRecordBytes = int64(len(`{"id":1}`))
		cfg.SkipOversize = true
		res, sink, err := runConvert(t, cfg)
		if err != nil {
			t.Fatalf("変換が失敗しました: %v", err)
		}
		assertLines(t, sink.lines(), []string{`{"id":1}`, `{"id":2}`})
		if res.Records != 2 {
			t.Errorf("Records が %d です(期待 2)", res.Records)
		}
	})

	t.Run("サイズ超過はスキップ指定がなければ位置不正", func(t *testing.T) {
		in := writeInput(t, "in.json", `[{"id":1},{"id":1234567890}]`)
		cfg := convertConfig(t, "", in)
		cfg.MaxRecordBytes = int64(len(`{"id":1}`))
		res, _, err := runConvert(t, cfg)
		if got := exitCodeOf(t, err); got != ExitLocation {
			t.Fatalf("終了コードが %d です(期待 %d): %v", got, ExitLocation, err)
		}
		if res != nil {
			t.Errorf("エラー時に結果 %+v が返されました(期待 nil)", res)
		}
	})
}

// --- (15) 整形パイプラインの結線(キー置換・固定値追加)--------------------------

func TestRunConversionAppliesPipeline(t *testing.T) {
	in := writeInput(t, "in.json", `[{"a-b":{"c-d":1}}]`)
	cfg := convertConfig(t, "", in)
	cfg.HyphenToUnder = true
	cfg.AddFields = []AddField{{Key: "src", Value: "page1"}}

	_, sink, err := runConvert(t, cfg)
	if err != nil {
		t.Fatalf("変換が失敗しました: %v", err)
	}
	assertLines(t, sink.lines(), []string{`{"a_b":{"c_d":1},"src":"page1"}`})
}

// --- (16) 書き込み境界のエラーはそのまま返す ------------------------------------

func TestRunConversionPropagatesSinkError(t *testing.T) {
	in := writeInput(t, "in.json", `[{"id":1},{"id":2},{"id":3}]`)
	want := &ExitError{Code: ExitOutput, Err: errors.New("書き込みに失敗しました")}
	sink := &testSink{failAt: 2, failErr: want}

	res, err := runConversion(convertConfig(t, "", in), sink)
	if !errors.Is(err, want) {
		t.Fatalf("シンクのエラーがそのまま返されていません: %v", err)
	}
	if got := exitCodeOf(t, err); got != ExitOutput {
		t.Errorf("終了コードが %d です(期待 %d)", got, ExitOutput)
	}
	if res != nil {
		t.Errorf("エラー時に結果 %+v が返されました(期待 nil)", res)
	}
	if len(sink.records) != 1 {
		t.Errorf("失敗前に書き込まれたのは %d 件です(期待 1 件)", len(sink.records))
	}
}

// --- (17) 入出力エラーと構文エラーの切り分け ------------------------------------

func TestIsInputIOError(t *testing.T) {
	pathErr := &fs.PathError{Op: "read", Path: "/x", Err: errors.New("入出力に失敗")}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "PathError は入出力エラー", err: pathErr, want: true},
		{name: "包まれた PathError も入出力エラー", err: errors.New("x"), want: true},
		{name: "素のエラーは入出力エラーではない", err: errors.New("構文エラー"), want: false},
		{name: "nil は入出力エラーではない", err: nil, want: false},
	}
	tests[1].err = &ExitError{Code: ExitInput, Err: pathErr}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isInputIOError(tt.err); got != tt.want {
				t.Errorf("isInputIOError(%v) = %v(期待 %v)", tt.err, got, tt.want)
			}
		})
	}
}

// --- (18) runConversionWithPipeline は警告の出力先を注入できる ------------------
// tasks.md「未配線の契約」: SetWarnWriter はタスク 5.1 の責務であり、
// convert は stderr を持たない。5.1 が Pipeline を組み立てて注入できること。

func TestRunConversionWithPipelineAcceptsConfiguredPipeline(t *testing.T) {
	in := writeInput(t, "in.json", `[{"id":1},{"id":1234567890}]`)
	cfg := convertConfig(t, "", in)
	cfg.MaxRecordBytes = int64(len(`{"id":1}`))
	cfg.SkipOversize = true

	var warn bytes.Buffer
	p := newPipeline(cfg)
	p.SetWarnWriter(&warn)

	sink := &testSink{}
	res, err := runConversionWithPipeline(cfg, sink, p)
	if err != nil {
		t.Fatalf("変換が失敗しました: %v", err)
	}
	assertLines(t, sink.lines(), []string{`{"id":1}`})
	if res.Records != 1 {
		t.Errorf("Records が %d です(期待 1)", res.Records)
	}
	if warn.Len() == 0 {
		t.Error("注入した警告出力先へ警告が書かれていません")
	}
}

// --- (19) 入力が 1 件もない場合 -------------------------------------------------

func TestRunConversionWithNoInputs(t *testing.T) {
	res, sink, err := runConvert(t, convertConfig(t, ""))
	if err != nil {
		t.Fatalf("変換が失敗しました: %v", err)
	}
	if res.Records != 0 || res.Files != 0 {
		t.Errorf("Records=%d Files=%d です(期待 0, 0)", res.Records, res.Files)
	}
	if len(sink.records) != 0 {
		t.Errorf("%d 件が書き込まれました(期待 0 件)", len(sink.records))
	}
}
