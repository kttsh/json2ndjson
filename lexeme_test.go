package main

// 字句保持と入力異常の常設テスト(タスク 2.4)。
//
// 要件定義書(docs/requirements.md 版 0.3)9.1 節「変換の正しさ」と 9.2 節「入力の異常」の
// 観点を、testdata 配下のフィクスチャで固定する。未決事項 Q-07(数値字句が保持されるか)と
// Q-09(Encoder が \uXXXX を書き直すか)の PoC を恒久化することがこのファイルの主目的であり、
// design.md「convert / Implementation Notes」の Validation・Risks が要求する常設テストにあたる。
//
// 対応する要件: 3.7(制御文字のエスケープ維持)、3.8(数値字句の無変換)、
// 3.9(\uXXXX の非導入とサロゲートペアの UTF-8 保持)、10.2(不正入力での安全な報告)。
//
// このファイルの最重要点は「素通し経路」と「再エンコード経路」の双方で結果が
// 1 バイトも変わらないことである。--key-hyphen-to-underscore や --add-field を
// 指定すると Pipeline.rewrite が Decoder→Encoder の往復を通るため、
// jsontext.Encoder の再フォーマット(research.md の Q-09)が字句を壊しうる。
// 素通し経路だけを検証しても、この最も危険な経路は素通りしてしまう。

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- フィクスチャ読み込みの補助 -------------------------------------------------

// fixture は testdata 配下のフィクスチャへの相対パスを返す。
// go test の作業ディレクトリはパッケージのディレクトリなので相対パスで開ける。
func fixture(name string) string {
	return filepath.Join("testdata", name)
}

// goldenLines は golden ファイル(1 行 1 レコード、LF 区切り、末尾に改行)を
// レコードの文字列スライスとして読む。
//
// レコードの内部に生の改行が現れないことは要件 3.2(FR-11)の保証であり、
// このテスト自身が assertNoRawControlBytes で別途確かめている。
// したがって LF による単純な分割で golden を復元できる。
func goldenLines(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile(fixture(name))
	if err != nil {
		t.Fatalf("golden %s を読めません: %v", name, err)
	}
	text := string(b)
	if text == "" {
		return nil
	}
	if !strings.HasSuffix(text, "\n") {
		t.Fatalf("golden %s が改行で終わっていません", name)
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// assertNoRawControlBytes は、書き出されたレコードに生の制御文字(0x20 未満)が
// 1 バイトも含まれないことを確かめる(要件 3.2 / FR-11)。
//
// レコード内に生の改行が 1 バイトでも混ざると NDJSON の「1 行 1 レコード」が崩れ、
// 受け側のロードが壊れる。字句保持のどのケースでもこの不変条件は破れてはならない。
func assertNoRawControlBytes(t *testing.T, records [][]byte) {
	t.Helper()
	for i, r := range records {
		for j, b := range r {
			if b < 0x20 {
				t.Fatalf("%d 件目のレコードの %d バイト目に生の制御文字 0x%02x があります: %q",
					i+1, j, b, r)
			}
		}
	}
}

// --- 9.1 節「変換の正しさ」のフィクスチャ表 -------------------------------------

// lexemeFixture は入力フィクスチャと期待出力(golden)の対応。
type lexemeFixture struct {
	// name はテスト名。要件定義書 9.1 節の観点と対応させる。
	name string
	// input は testdata 配下の入力ファイル名。
	input string
	// path は --path。空文字列はルート。
	path string
	// golden は整形なし(素通し経路)で期待する出力ファイル名。
	golden string
	// underGolden は --key-hyphen-to-underscore を指定したときの期待出力。
	// 空文字列は「golden と 1 バイトも変わらない」ことを要求する。
	underGolden string
}

// lexemeFixtures はレコードがトップレベルオブジェクトであるフィクスチャ。
// 素通し・キー名置換・固定値追加の 3 経路すべてで検証する。
var lexemeFixtures = []lexemeFixture{
	{
		name:   "9.1 数値字句を入力のまま出す",
		input:  "numbers.json",
		golden: "numbers.ndjson",
	},
	{
		name:   "9.1 不必要な uXXXX 化を行わない",
		input:  "escapes.json",
		golden: "escapes.ndjson",
	},
	{
		name:   "9.1 サロゲートペアを UTF-8 のまま保つ",
		input:  "surrogates.json",
		golden: "surrogates.ndjson",
	},
	{
		name:   "9.1 制御文字のエスケープを維持する",
		input:  "control.json",
		golden: "control.ndjson",
	},
	{
		name:   "9.1 null 真偽値 金額文字列 日時を変換しない",
		input:  "scalars.json",
		golden: "scalars.ndjson",
	},
	{
		name:   "9.1 深いネストと配列内配列を保持する",
		input:  "nested.json",
		golden: "nested.ndjson",
	},
	{
		name:        "9.1 キー名のハイフン 空白 日本語を正しく出す",
		input:       "keys.json",
		golden:      "keys.ndjson",
		underGolden: "keys_under.ndjson",
	},
	{
		name:   "9.1 単一オブジェクトが 1 行になる",
		input:  "single.json",
		golden: "single.ndjson",
	},
	{
		name:   "9.2 BOM 付き入力を処理できる",
		input:  "bom.json",
		path:   "/data",
		golden: "envelope.ndjson",
	},
	{
		name:   "9.2 CRLF 入力を処理できる",
		input:  "crlf.json",
		path:   "/data",
		golden: "envelope.ndjson",
	},
	{
		name:   "9.2 BOM も CRLF もない入力を処理できる",
		input:  "plain.json",
		path:   "/data",
		golden: "envelope.ndjson",
	},
}

// TestFixturePassThroughPreservesLexemes は整形オプションなしの経路
// (Compact 済み生バイトをそのまま書く経路)を検証する。
func TestFixturePassThroughPreservesLexemes(t *testing.T) {
	for _, tt := range lexemeFixtures {
		t.Run(tt.name, func(t *testing.T) {
			want := goldenLines(t, tt.golden)
			res, sink, err := runConvert(t, convertConfig(t, tt.path, fixture(tt.input)))
			if err != nil {
				t.Fatalf("変換が失敗しました: %v", err)
			}
			assertLines(t, sink.lines(), want)
			assertNoRawControlBytes(t, sink.records)
			if res.Records != int64(len(want)) {
				t.Errorf("Records が %d です(期待 %d)", res.Records, len(want))
			}
		})
	}
}

// TestFixtureRewritePathPreservesLexemes は --key-hyphen-to-underscore を有効にして
// Pipeline.rewrite の Decoder→Encoder 往復を通す経路を検証する。
//
// underGolden が空のフィクスチャ(= キー名にハイフンがないもの)では、
// 素通し経路とまったく同じ golden を要求する。これが「再エンコードしても
// 1 バイトも変わらない」という要件 3.7〜3.9 の実質的な保証になる。
func TestFixtureRewritePathPreservesLexemes(t *testing.T) {
	for _, tt := range lexemeFixtures {
		t.Run(tt.name, func(t *testing.T) {
			golden := tt.underGolden
			if golden == "" {
				golden = tt.golden
			}
			want := goldenLines(t, golden)

			cfg := convertConfig(t, tt.path, fixture(tt.input))
			cfg.HyphenToUnder = true
			res, sink, err := runConvert(t, cfg)
			if err != nil {
				t.Fatalf("変換が失敗しました: %v", err)
			}
			assertLines(t, sink.lines(), want)
			assertNoRawControlBytes(t, sink.records)
			if res.Records != int64(len(want)) {
				t.Errorf("Records が %d です(期待 %d)", res.Records, len(want))
			}
		})
	}
}

// withAddedField は golden の 1 行に --add-field の結果を足した期待値を返す。
//
// 固定値追加はトップレベルオブジェクトの末尾へ "キー":"値" を書き足すだけであり、
// 既存部分のバイト列には触れない。そのため期待値は golden から機械的に導ける。
func withAddedField(t *testing.T, line, key, value string) string {
	t.Helper()
	if !strings.HasSuffix(line, "}") || line == "{}" {
		t.Fatalf("golden の行 %q が空でないトップレベルオブジェクトではありません", line)
	}
	return line[:len(line)-1] + `,"` + key + `":"` + value + `"}`
}

// TestFixtureAddFieldPathPreservesLexemes は --add-field を有効にして
// Pipeline.copyTopLevelObject 経由の再エンコードを通す経路を検証する。
//
// 追加されるのは末尾の 1 フィールドだけで、既存レコードの字句は変わらない。
func TestFixtureAddFieldPathPreservesLexemes(t *testing.T) {
	const (
		addKey   = "batch"
		addValue = "20260825T01"
	)
	for _, tt := range lexemeFixtures {
		t.Run(tt.name, func(t *testing.T) {
			golden := goldenLines(t, tt.golden)
			want := make([]string, 0, len(golden))
			for _, line := range golden {
				want = append(want, withAddedField(t, line, addKey, addValue))
			}

			cfg := convertConfig(t, tt.path, fixture(tt.input))
			cfg.AddFields = []AddField{{Key: addKey, Value: addValue}}
			_, sink, err := runConvert(t, cfg)
			if err != nil {
				t.Fatalf("変換が失敗しました: %v", err)
			}
			assertLines(t, sink.lines(), want)
			assertNoRawControlBytes(t, sink.records)
		})
	}
}

// --- 9.1 節 uXXXX の非導入とサロゲートペア(要件 3.9 / FR-18)--------------------

// JSON のエスケープ列は Go のソース上で解釈済み文字へ展開されうるため、
// 生の文字は rune から、エスケープ列は逆斜線を明示した文字列から組み立てる。
const (
	rawEAcute  = string(rune(0x00E9))  // é
	rawEmoji   = string(rune(0x1F600)) // 絵文字
	rawLineSep = string(rune(0x2028))  // LINE SEPARATOR
	rawParaSep = string(rune(0x2029))  // PARAGRAPH SEPARATOR

	// escapeMarker は \uXXXX エスケープの開始 2 バイト。
	escapeMarker = "\\u"
	// escapedEmoji は絵文字をサロゲートペアでエスケープした 12 バイト。
	escapedEmoji = "\\ud83d\\ude00"
	// escapedEAcute は é を BMP のエスケープで書いた 6 バイト。
	escapedEAcute = "\\u00e9"
)

// convertPaths は同じ入力を素通し経路・キー名置換経路・固定値追加経路の
// 3 通りで変換し、それぞれのレコード列を返す。
//
// 字句の保証は経路によらず成立しなければならない(design.md「convert / Risks」)。
func convertPaths(t *testing.T, input, path string) map[string][]string {
	t.Helper()
	out := make(map[string][]string, 3)

	_, plain, err := runConvert(t, convertConfig(t, path, fixture(input)))
	if err != nil {
		t.Fatalf("素通し経路の変換が失敗しました: %v", err)
	}
	out["素通し"] = plain.lines()

	under := convertConfig(t, path, fixture(input))
	under.HyphenToUnder = true
	_, underSink, err := runConvert(t, under)
	if err != nil {
		t.Fatalf("キー名置換経路の変換が失敗しました: %v", err)
	}
	out["キー名置換"] = underSink.lines()

	add := convertConfig(t, path, fixture(input))
	add.AddFields = []AddField{{Key: "batch", Value: "1"}}
	_, addSink, err := runConvert(t, add)
	if err != nil {
		t.Fatalf("固定値追加経路の変換が失敗しました: %v", err)
	}
	out["固定値追加"] = addSink.lines()

	return out
}

// TestFixtureDoesNotIntroduceUnicodeEscapes は、素のまま渡された文字が
// どの経路でも \uXXXX へ書き換えられないことを確かめる(要件 3.9 / FR-18)。
//
// 素朴なエンコーダは HTML 安全化のために不等号やアンパサンドを、
// JavaScript 互換のために U+2028 / U+2029 を uXXXX 形式へ書き換えることがある
// (標準ライブラリの encoding/json v1 の既定がこれにあたる)。
// 本ツールはそのいずれも行ってはならない。
func TestFixtureDoesNotIntroduceUnicodeEscapes(t *testing.T) {
	rawWanted := []string{"<", ">", "&", "<script> & </script>", rawEAcute, rawEmoji, rawLineSep, rawParaSep}

	for route, lines := range convertPaths(t, "escapes.json", "") {
		joined := strings.Join(lines, "\n")
		if strings.Contains(joined, escapeMarker) {
			t.Errorf("%s経路の出力に %s エスケープが導入されています: %q", route, escapeMarker, joined)
		}
		for _, want := range rawWanted {
			if !strings.Contains(joined, want) {
				t.Errorf("%s経路の出力に素の %q が含まれていません: %q", route, want, joined)
			}
		}
	}
}

// TestFixtureSurrogatePairsSurviveBothDirections は、サロゲートペアの
// 「エスケープされた形」と「生の UTF-8」の双方がそのまま保たれることを
// 確かめる(要件 3.9)。保証は「常にどちらか一方の形にする」ではなく
// 「入力の形を変えない」であるため、両方向を固定する必要がある。
func TestFixtureSurrogatePairsSurviveBothDirections(t *testing.T) {
	for route, lines := range convertPaths(t, "surrogates.json", "") {
		if len(lines) != 5 {
			t.Fatalf("%s経路のレコード数が %d 件です(期待 5 件)", route, len(lines))
		}

		// 1 件目: エスケープされたサロゲートペアは展開されない。
		if !strings.Contains(lines[0], escapedEmoji) {
			t.Errorf("%s経路の 1 件目が %s を保っていません: %q", route, escapedEmoji, lines[0])
		}
		if strings.Contains(lines[0], rawEmoji) {
			t.Errorf("%s経路の 1 件目でエスケープが生の絵文字へ展開されています: %q", route, lines[0])
		}

		// 2 件目: 生の絵文字はエスケープされない。
		if !strings.Contains(lines[1], rawEmoji) {
			t.Errorf("%s経路の 2 件目が生の絵文字を保っていません: %q", route, lines[1])
		}
		if strings.Contains(lines[1], escapeMarker) {
			t.Errorf("%s経路の 2 件目で生の絵文字がエスケープされています: %q", route, lines[1])
		}

		// 3 件目 / 4 件目: BMP でも同じ双方向の保証が成り立つ。
		if !strings.Contains(lines[2], escapedEAcute) {
			t.Errorf("%s経路の 3 件目が %s を保っていません: %q", route, escapedEAcute, lines[2])
		}
		if !strings.Contains(lines[3], rawEAcute) || strings.Contains(lines[3], escapeMarker) {
			t.Errorf("%s経路の 4 件目が生の é を保っていません: %q", route, lines[3])
		}

		// 5 件目: 同一レコード内に両形式が混在しても、どちらも変換されない。
		if !strings.Contains(lines[4], rawEmoji) || !strings.Contains(lines[4], escapedEmoji) {
			t.Errorf("%s経路の 5 件目が両形式を保っていません: %q", route, lines[4])
		}
	}
}

// TestFixtureControlEscapesSurviveEveryRoute は、二文字エスケープと
// \uXXXX 形式の制御文字がどの経路でも書き換えられないことを確かめる
// (要件 3.7 / FR-11・FR-16)。
func TestFixtureControlEscapesSurviveEveryRoute(t *testing.T) {
	// 入力に現れる二文字エスケープ。生の制御文字へ展開されてはならない。
	twoChar := []string{`\t`, `\n`, `\r`, `\b`, `\f`, `\"`, `\\`, `\/`}

	for route, lines := range convertPaths(t, "control.json", "") {
		joined := strings.Join(lines, "\n")
		for _, esc := range twoChar {
			if !strings.Contains(joined, esc) {
				t.Errorf("%s経路の出力に二文字エスケープ %s が残っていません: %q", route, esc, joined)
			}
		}
		for _, esc := range []string{escapeMarker + "0000", escapeMarker + "0001", escapeMarker + "001f", escapeMarker + "0007"} {
			if !strings.Contains(joined, esc) {
				t.Errorf("%s経路の出力に %s が残っていません: %q", route, esc, joined)
			}
		}
		// 行そのものに生の制御文字が現れないこと(NDJSON の 1 行 1 レコードの前提)。
		for i, line := range lines {
			for j := range len(line) {
				if line[j] < 0x20 {
					t.Fatalf("%s経路の %d 件目の %d バイト目に生の制御文字 0x%02x があります: %q",
						route, i+1, j, line[j], line)
				}
			}
		}
	}
}

// TestFixtureNumberLexemesSurviveEveryRoute は、float64 の往復で必ず変質する
// 数値字句が、どの経路でも 1 バイトも変わらないことを確かめる(要件 3.8 / FR-17)。
func TestFixtureNumberLexemesSurviveEveryRoute(t *testing.T) {
	// 入力にそのまま現れるべき字句。丸め・正規化・指数表記化のいずれも起きてはならない。
	lexemes := []string{
		"1.0",
		"1e10",
		"1E+10",
		"-0",
		"0.0",
		"9223372036854775808",
		"123456789012345678901234567890",
		"123456789012345678901234567890.123456789",
		"0.12345678901234567890",
		"-1.5E+300",
		"[1,1.0,1.00,1e0,1E0,10e-1]",
	}

	for route, lines := range convertPaths(t, "numbers.json", "") {
		joined := strings.Join(lines, "\n")
		for _, lex := range lexemes {
			if !strings.Contains(joined, lex) {
				t.Errorf("%s経路の出力に数値字句 %s が現れません: %q", route, lex, joined)
			}
		}
	}
}

// --- 10.2 ネスト深さの境界(511 可・512 可・513 不可)---------------------------

// TestFixtureNestingDepthBoundary はフィクスチャによる境界の固定。
//
// タスク 2.3 の TestRunConversionNestingDepthBoundary は生成した配列のみの入れ子を
// 使うが、ここではオブジェクトと配列を交互に入れ子にしたファイルで同じ境界を確かめる
// (深さの数え方が括弧の種類に依存しないことの補完)。
func TestFixtureNestingDepthBoundary(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		golden string // 空なら深さ超過でエラーを期待する
	}{
		{name: "深さ 511 は許可", input: "depth511.json", golden: "depth511.ndjson"},
		{name: "深さ 512 は許可(上限そのもの)", input: "depth512.json", golden: "depth512.ndjson"},
		{name: "深さ 513 は構文エラー", input: "depth513.json"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, sink, err := runConvert(t, convertConfig(t, "/data", fixture(tt.input)))
			if tt.golden != "" {
				if err != nil {
					t.Fatalf("%s が拒否されました: %v", tt.input, err)
				}
				assertLines(t, sink.lines(), goldenLines(t, tt.golden))
				return
			}

			if got := exitCodeOf(t, err); got != ExitSyntax {
				t.Fatalf("終了コードが %d です(期待 %d): %v", got, ExitSyntax, err)
			}
			msg := err.Error()
			for _, want := range []string{filepath.Base(tt.input), "512"} {
				if !strings.Contains(msg, want) {
					t.Errorf("エラーメッセージに %q が含まれていません: %s", want, msg)
				}
			}
			assertHasLocation(t, msg, "/data/0")
			if len(sink.records) != 0 {
				t.Errorf("深さ超過なのに %d 件が書き込まれました", len(sink.records))
			}
		})
	}
}

// --- 9.2 不正な JSON の位置報告(要件 1.6 / FR-38)-------------------------------

// assertHasLocation はエラーメッセージがバイトオフセットと JSON Pointer を
// 備えていることを確かめる。wantPointer が空でなければ前方一致も検査する。
func assertHasLocation(t *testing.T, msg, wantPointer string) {
	t.Helper()
	m := byteOffsetPattern.FindStringSubmatch(msg)
	if m == nil {
		t.Fatalf("エラーメッセージにバイトオフセットが含まれていません: %s", msg)
	}
	if m[1] == "0" {
		t.Errorf("バイトオフセットが 0 です(0 より大きい値を期待): %s", msg)
	}
	p := jsonPointerPatter.FindStringSubmatch(msg)
	if p == nil {
		t.Fatalf("エラーメッセージに JSON Pointer が含まれていません: %s", msg)
	}
	if wantPointer != "" && !strings.HasPrefix(p[1], wantPointer) {
		t.Errorf("JSON Pointer が %q です(%q で始まることを期待): %s", p[1], wantPointer, msg)
	}
}

// TestFixtureMalformedJSONReportsLocation は、不正な JSON が終了コード 3 になり、
// ファイル名・バイトオフセット・JSON Pointer のすべてが報告されることを確かめる。
func TestFixtureMalformedJSONReportsLocation(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		path        string
		wantPointer string // 空なら Pointer の値は問わない(存在だけを確かめる)
	}{
		{name: "文書が途中で切れている", input: "bad_truncated.json", path: "/data"},
		{name: "文字列が閉じていない", input: "bad_unterminated_string.json", path: "/data"},
		{name: "配列に余分なカンマがある", input: "bad_trailing_comma.json", path: "/data"},
		{name: "引用符のない語がある", input: "bad_bare_word.json", path: "/data"},
		{name: "メンバの値が欠けている", input: "bad_missing_value.json", path: "/data", wantPointer: "/data/1"},
		{name: "対象の後ろにごみが付いている", input: "bad_trailing_garbage.json", path: "/data"},
		{name: "対象の後ろの兄弟の値が壊れている", input: "bad_sibling.json", path: "/data"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, sink, err := runConvert(t, convertConfig(t, tt.path, fixture(tt.input)))
			if got := exitCodeOf(t, err); got != ExitSyntax {
				t.Fatalf("終了コードが %d です(期待 %d): %v", got, ExitSyntax, err)
			}
			if res != nil {
				t.Errorf("エラー時に結果 %+v が返されました(期待 nil)", res)
			}
			msg := err.Error()
			if !strings.Contains(msg, filepath.Base(tt.input)) {
				t.Errorf("エラーメッセージにファイル名が含まれていません: %s", msg)
			}
			assertHasLocation(t, msg, tt.wantPointer)
			assertNoRawControlBytes(t, sink.records)
		})
	}
}

// --- 9.2 その他の入力異常 -------------------------------------------------------

// TestFixtureInvalidPathLocation は --path が指す位置が存在しない、
// または配列でもオブジェクトでもない場合に終了コード 5 になることを確かめる
// (要件 2.4 / FR-08)。
func TestFixtureInvalidPathLocation(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "文字列を指している", path: "/note"},
		{name: "null を指している", path: "/nothing"},
		{name: "数値を指している", path: "/count"},
		{name: "真偽値を指している", path: "/flag"},
		{name: "存在しないキーを指している", path: "/missing"},
		{name: "配列の範囲外を指している", path: "/data/1"},
		{name: "スカラーの内側へ降りようとしている", path: "/note/0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, sink, err := runConvert(t, convertConfig(t, tt.path, fixture("locations.json")))
			if got := exitCodeOf(t, err); got != ExitLocation {
				t.Fatalf("終了コードが %d です(期待 %d): %v", got, ExitLocation, err)
			}
			if res != nil {
				t.Errorf("エラー時に結果 %+v が返されました(期待 nil)", res)
			}
			if len(sink.records) != 0 {
				t.Errorf("位置不正なのに %d 件が書き込まれました", len(sink.records))
			}
			if !strings.Contains(err.Error(), filepath.Base("locations.json")) {
				t.Errorf("エラーメッセージにファイル名が含まれていません: %s", err)
			}
		})
	}

	// 正しい位置は同じフィクスチャで通ること(上の異常系が「常に 5 を返すだけ」
	// の実装で通ってしまわないための対照)。
	_, sink, err := runConvert(t, convertConfig(t, "/data", fixture("locations.json")))
	if err != nil {
		t.Fatalf("正しい --path で変換が失敗しました: %v", err)
	}
	assertLines(t, sink.lines(), goldenLines(t, "locations.ndjson"))
}

// TestFixtureErrorResponseIsNotInterpreted は、エラー応答らしい JSON でも
// 内容による自動判定を行わないことを確かめる(要件 2.5 / FR-09)。
//
// --path / は「空文字列のキー」を指すため、そのようなキーを持たない
// エラー応答では終了コード 5 になる(9.2 節の観点)。一方 --path 省略
// (ルート)なら、ツールは中身を解釈せずオブジェクト 1 件として出力する。
func TestFixtureErrorResponseIsNotInterpreted(t *testing.T) {
	res, sink, err := runConvert(t, convertConfig(t, "/", fixture("error_response.json")))
	if got := exitCodeOf(t, err); got != ExitLocation {
		t.Fatalf("--path / の終了コードが %d です(期待 %d): %v", got, ExitLocation, err)
	}
	if res != nil {
		t.Errorf("エラー時に結果 %+v が返されました(期待 nil)", res)
	}
	if len(sink.records) != 0 {
		t.Errorf("位置不正なのに %d 件が書き込まれました", len(sink.records))
	}

	res, sink, err = runConvert(t, convertConfig(t, "", fixture("error_response.json")))
	if err != nil {
		t.Fatalf("ルート指定の変換が失敗しました: %v", err)
	}
	assertLines(t, sink.lines(), goldenLines(t, "error_response.ndjson"))
	if res.Records != 1 {
		t.Errorf("Records が %d です(期待 1)", res.Records)
	}
}

// TestFixtureEmptyArrayEmitsNoRecords は空配列で 1 件も出力せず正常終了することを
// 確かめる(要件 3.10 / FR-19、9.1 節の観点)。
// 0 バイトの出力ファイルを作る側の保証はタスク 3.2(output)の責務。
func TestFixtureEmptyArrayEmitsNoRecords(t *testing.T) {
	res, sink, err := runConvert(t, convertConfig(t, "/data", fixture("empty.json")))
	if err != nil {
		t.Fatalf("空配列で変換が失敗しました: %v", err)
	}
	if len(sink.records) != 0 {
		t.Errorf("%d 件が書き込まれました(期待 0 件)", len(sink.records))
	}
	if res.Records != 0 || res.Files != 1 {
		t.Errorf("結果が Records=%d Files=%d です(期待 Records=0 Files=1)", res.Records, res.Files)
	}
	if res.LastValue != nil {
		t.Errorf("LastValue が %q です(期待 nil)", res.LastValue)
	}
}

// TestFixtureMissingInputFile は存在しない入力ファイルで終了コード 2 に
// なることを確かめる(要件 1.5、9.2 節の観点)。
func TestFixtureMissingInputFile(t *testing.T) {
	missing := fixture("no_such_fixture.json")
	if _, err := os.Stat(missing); err == nil {
		t.Fatalf("%s が存在します(存在しないことを前提としたテストです)", missing)
	}
	res, sink, err := runConvert(t, convertConfig(t, "", missing))
	if got := exitCodeOf(t, err); got != ExitInput {
		t.Fatalf("終了コードが %d です(期待 %d): %v", got, ExitInput, err)
	}
	if res != nil {
		t.Errorf("エラー時に結果 %+v が返されました(期待 nil)", res)
	}
	if len(sink.records) != 0 {
		t.Errorf("%d 件が書き込まれました(期待 0 件)", len(sink.records))
	}
	if !strings.Contains(err.Error(), filepath.Base(missing)) {
		t.Errorf("エラーメッセージにファイル名が含まれていません: %s", err)
	}
}

// --- 経路をまたいだ同一性の直接確認 ---------------------------------------------

// TestFixtureRoutesAgreeByteForByte は、キー名にハイフンを含まないフィクスチャで
// 「素通し経路」と「キー名置換経路」が 1 バイトも違わないことを直接確かめる。
//
// 個別の golden 比較でも同じことを固定しているが、この検査は golden を介さずに
// 2 つの経路を直接突き合わせるため、golden 側の取り違えでは通らない。
func TestFixtureRoutesAgreeByteForByte(t *testing.T) {
	for _, tt := range lexemeFixtures {
		if tt.underGolden != "" {
			continue // キー名が置換されるフィクスチャは対象外
		}
		t.Run(tt.name, func(t *testing.T) {
			_, plain, err := runConvert(t, convertConfig(t, tt.path, fixture(tt.input)))
			if err != nil {
				t.Fatalf("素通し経路の変換が失敗しました: %v", err)
			}
			cfg := convertConfig(t, tt.path, fixture(tt.input))
			cfg.HyphenToUnder = true
			_, rewritten, err := runConvert(t, cfg)
			if err != nil {
				t.Fatalf("再エンコード経路の変換が失敗しました: %v", err)
			}
			if len(plain.records) != len(rewritten.records) {
				t.Fatalf("レコード数が経路で異なります(素通し %d 件、再エンコード %d 件)",
					len(plain.records), len(rewritten.records))
			}
			for i := range plain.records {
				if !bytes.Equal(plain.records[i], rewritten.records[i]) {
					t.Errorf("%d 件目が経路で異なります\n  素通し    : %q\n  再エンコード: %q",
						i+1, plain.records[i], rewritten.records[i])
				}
			}
		})
	}
}
