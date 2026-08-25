package main

import (
	"slices"
	"testing"
)

// TestParsePointer は RFC 6901 に準拠した参照トークン列への解析を検証する(要件 2.1、
// design.md「pointer(RFC 6901 JSON Pointer)」)。
// RFC 6901 §5 の全例と、エスケープ解除の境界ケースを網羅する。
func TestParsePointer(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		// 空文字列はルート。トークンを 1 つも持たない。
		{"空文字列はルート", "", []string{}},

		// スラッシュ 1 つは「空文字列というキー」を指す 1 トークン。ルートとは異なる。
		{"スラッシュのみは空文字列トークン 1 個", "/", []string{""}},

		// 基本形。
		{"単一トークン", "/foo", []string{"foo"}},
		{"複数トークン", "/foo/bar", []string{"foo", "bar"}},
		{"末尾スラッシュは空文字列トークンを生む", "/a/", []string{"a", ""}},
		{"連続スラッシュは空文字列トークン 2 個", "//", []string{"", ""}},
		{"深いネスト", "/a/b/c/d", []string{"a", "b", "c", "d"}},

		// RFC 6901 §5 の例。
		{"RFC 6901 §5 /foo", "/foo", []string{"foo"}},
		{"RFC 6901 §5 /foo/0", "/foo/0", []string{"foo", "0"}},
		{"RFC 6901 §5 /", "/", []string{""}},
		{"RFC 6901 §5 /a~1b(~1 はスラッシュ)", "/a~1b", []string{"a/b"}},
		{"RFC 6901 §5 /c%d", "/c%d", []string{"c%d"}},
		{"RFC 6901 §5 /e^f", "/e^f", []string{"e^f"}},
		{"RFC 6901 §5 /g|h", "/g|h", []string{"g|h"}},
		{`RFC 6901 §5 /i\j`, `/i\j`, []string{`i\j`}},
		{`RFC 6901 §5 /k"l`, `/k"l`, []string{`k"l`}},
		{"RFC 6901 §5 /(空白)", "/ ", []string{" "}},
		{"RFC 6901 §5 /m~0n(~0 はチルダ)", "/m~0n", []string{"m~n"}},

		// エスケープ解除の境界ケース。単純な連続置換では誤る組み合わせ。
		{"~01 は ~1 へ復号する(~0 の生成物を再走査しない)", "/~01", []string{"~1"}},
		{"~10 は /0 へ復号する", "/~10", []string{"/0"}},
		{"~0~1 は ~/ へ復号する", "/~0~1", []string{"~/"}},
		{"~1~0 は /~ へ復号する", "/~1~0", []string{"/~"}},
		{"~00 は ~0 へ復号する", "/~00", []string{"~0"}},
		{"~0 のみのトークン", "/~0", []string{"~"}},
		{"~1 のみのトークン", "/~1", []string{"/"}},
		{"エスケープを含むトークンが複数", "/a~1b/c~0d", []string{"a/b", "c~d"}},

		// 添字らしきトークンは解析段階では単なるトークンとして通す。
		{"配列添字トークン", "/items/0", []string{"items", "0"}},
		{"先頭ゼロのトークンはオブジェクトキーとして有効", "/items/01", []string{"items", "01"}},

		// 非 ASCII。バイト単位の走査で壊れないこと。
		{"日本語キー", "/データ/件数", []string{"データ", "件数"}},
		{"絵文字キー", "/🍣", []string{"🍣"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePointer(tt.in)
			if err != nil {
				t.Fatalf("ParsePointer(%q) が予期しないエラーを返した: %v", tt.in, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("ParsePointer(%q) のトークン数 = %d (%q), want %d (%q)",
					tt.in, len(got), []string(got), len(tt.want), tt.want)
			}
			if !slices.Equal([]string(got), tt.want) {
				t.Errorf("ParsePointer(%q) = %q, want %q", tt.in, []string(got), tt.want)
			}
		})
	}
}

// TestParsePointerRootIsNotEmptyToken はルート("")と空文字列キー("/")を
// 取り違えていないことを明示的に検証する。
// 両者を混同すると、ルート指定時に存在しないキー "" を探しにいく不具合になる。
func TestParsePointerRootIsNotEmptyToken(t *testing.T) {
	root, err := ParsePointer("")
	if err != nil {
		t.Fatalf(`ParsePointer("") が予期しないエラーを返した: %v`, err)
	}
	if len(root) != 0 {
		t.Errorf(`ParsePointer("") のトークン数 = %d (%q), want 0`, len(root), []string(root))
	}

	emptyKey, err := ParsePointer("/")
	if err != nil {
		t.Fatalf(`ParsePointer("/") が予期しないエラーを返した: %v`, err)
	}
	if len(emptyKey) != 1 {
		t.Fatalf(`ParsePointer("/") のトークン数 = %d (%q), want 1`, len(emptyKey), []string(emptyKey))
	}
	if emptyKey[0] != "" {
		t.Errorf(`ParsePointer("/")[0] = %q, want ""`, emptyKey[0])
	}
}

// TestParsePointerInvalid は不正形式がエラーとして報告されることを検証する(要件 2.1)。
// 呼び出し側(cli)がこのエラーを終了コード 1 へ写像する。
func TestParsePointerInvalid(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"先頭がスラッシュでない", "foo"},
		{"先頭がスラッシュでない(複数トークン風)", "foo/bar"},
		{"先頭が空白", " /foo"},
		{"チルダ単独(ポインタ全体)", "~"},
		{"チルダ単独(トークン末尾)", "/~"},
		{"チルダ単独(トークン途中の末尾)", "/a~"},
		{"未定義のエスケープ ~2", "/~2"},
		{"未定義のエスケープ ~x", "/~x"},
		{"未定義のエスケープ ~~", "/~~"},
		{"チルダの直後がスラッシュ", "/~/a"},
		{"後続トークンに不正なエスケープ", "/a/~9"},
		{"非 ASCII が続くチルダ", "/~あ"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePointer(tt.in)
			if err == nil {
				t.Fatalf("ParsePointer(%q) = %q, nil; want エラー", tt.in, []string(got))
			}
			if got != nil {
				t.Errorf("ParsePointer(%q) はエラー時に非 nil の Pointer %q を返した", tt.in, []string(got))
			}
			if err.Error() == "" {
				t.Error("エラーメッセージが空文字列")
			}
		})
	}
}

// TestParsePointerResultIsIndependent は返された Pointer が呼び出しごとに独立で、
// 内部状態を共有しないことを検証する(design.md「解析は入力文字列のみに依存し副作用を持たない」)。
func TestParsePointerResultIsIndependent(t *testing.T) {
	const in = "/a/b"

	first, err := ParsePointer(in)
	if err != nil {
		t.Fatalf("ParsePointer(%q) が予期しないエラーを返した: %v", in, err)
	}
	first[0] = "書き換え"
	extended := append(first, "追加")
	if len(extended) != 3 {
		t.Fatalf("append 後の長さ = %d, want 3", len(extended))
	}

	second, err := ParsePointer(in)
	if err != nil {
		t.Fatalf("ParsePointer(%q)(2 回目)が予期しないエラーを返した: %v", in, err)
	}
	if want := []string{"a", "b"}; !slices.Equal([]string(second), want) {
		t.Errorf("1 回目の結果を書き換えた後の ParsePointer(%q) = %q, want %q", in, []string(second), want)
	}
}

// TestParseArrayIndex は参照トークンを配列添字として解釈する規則(RFC 6901 §4)を
// 検証する。添字は "0" または [1-9][0-9]* のみで、先頭ゼロは禁止(要件 2.1)。
func TestParseArrayIndex(t *testing.T) {
	tests := []struct {
		name   string
		token  string
		want   int
		wantOK bool
	}{
		{"ゼロ", "0", 0, true},
		{"一桁", "1", 1, true},
		{"二桁", "10", 10, true},
		{"複数桁", "42", 42, true},
		{"大きな値", "1234567890", 1234567890, true},

		{"先頭ゼロ", "01", 0, false},
		{"ゼロの重複", "00", 0, false},
		{"先頭ゼロ(三桁)", "007", 0, false},
		{"負数", "-1", 0, false},
		{"正符号付き", "+1", 0, false},
		{"小数", "1.0", 0, false},
		{"空文字列", "", 0, false},
		{"英字", "abc", 0, false},
		{"RFC 6901 の末尾記号 -", "-", 0, false},
		{"前後の空白", " 1", 0, false},
		{"末尾の空白", "1 ", 0, false},
		{"指数表記", "1e2", 0, false},
		{"全角数字", "１", 0, false},
		{"数字と英字の混在", "1a", 0, false},
		{"int の範囲を超える値", "99999999999999999999999", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseArrayIndex(tt.token)
			if ok != tt.wantOK {
				t.Fatalf("parseArrayIndex(%q) の可否 = %t, want %t", tt.token, ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("parseArrayIndex(%q) = %d, want %d", tt.token, got, tt.want)
			}
		})
	}
}

// TestParseArrayIndexFromParsedPointer は ParsePointer が返したトークンを
// そのまま添字判定へ渡せること、および先頭ゼロのトークンがポインタとしては
// 有効(オブジェクトキー)でありながら添字としては無効であることを検証する。
// 添字規則の適用は値が配列だと分かった時点(タスク 2.2/2.3)で行う。
func TestParseArrayIndexFromParsedPointer(t *testing.T) {
	p, err := ParsePointer("/items/0/tags/01")
	if err != nil {
		t.Fatalf("ParsePointer が予期しないエラーを返した: %v", err)
	}
	if len(p) != 4 {
		t.Fatalf("トークン数 = %d (%q), want 4", len(p), []string(p))
	}

	if idx, ok := parseArrayIndex(p[1]); !ok || idx != 0 {
		t.Errorf("parseArrayIndex(%q) = %d, %t; want 0, true", p[1], idx, ok)
	}
	if idx, ok := parseArrayIndex(p[3]); ok {
		t.Errorf("parseArrayIndex(%q) = %d, true; want false(先頭ゼロは添字ではない)", p[3], idx)
	}
}
