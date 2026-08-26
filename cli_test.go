package main

import (
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// cliPointer はテスト用に JSON Pointer を解析する。解析失敗はテストの前提崩壊。
func cliPointer(t *testing.T, s string) Pointer {
	t.Helper()
	p, err := ParsePointer(s)
	if err != nil {
		t.Fatalf("テスト前提の ParsePointer(%q) が失敗しました: %v", s, err)
	}
	return p
}

// cliPointerRef は *Pointer を返す(Config.CursorKey / Config.DedupeKey 用)。
func cliPointerRef(t *testing.T, s string) *Pointer {
	t.Helper()
	p := cliPointer(t, s)
	return &p
}

// captureStdio は fn の実行中に os.Stdout / os.Stderr へ書かれた内容を捕捉する。
// 要件 8.1(標準出力は結果行のみ)と design.md「stdout を汚さない」の検証に使う。
func captureStdio(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe に失敗しました: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe に失敗しました: %v", err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	defer func() {
		os.Stdout, os.Stderr = origOut, origErr
	}()

	fn()

	if err := outW.Close(); err != nil {
		t.Fatalf("stdout パイプの Close に失敗しました: %v", err)
	}
	if err := errW.Close(); err != nil {
		t.Fatalf("stderr パイプの Close に失敗しました: %v", err)
	}
	outB, err := io.ReadAll(outR)
	if err != nil {
		t.Fatalf("stdout の読み取りに失敗しました: %v", err)
	}
	errB, err := io.ReadAll(errR)
	if err != nil {
		t.Fatalf("stderr の読み取りに失敗しました: %v", err)
	}
	return string(outB), string(errB)
}

// baseConfig は最小の正常引数に対応する期待値を返す。
// 既定値(要件 3.3 / 3.5、design.md「cli(引数解析)」)を 1 箇所に集約する。
func baseConfig(t *testing.T) *Config {
	t.Helper()
	return &Config{
		Inputs:          []string{"/in/a.json"},
		Out:             "/out/o.ndjson",
		Path:            cliPointer(t, ""),
		Newline:         "\n",
		TrailingNewline: true,
	}
}

// TestParseArgsValidMatrix は正常系の組み合わせマトリクス(要件 1.2、4.1、6.3、7.1)。
func TestParseArgsValidMatrix(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want func(t *testing.T) *Config
	}{
		{
			name: "最小構成(既定値の確定)",
			args: []string{"--in", "/in/a.json", "--out", "/out/o.ndjson"},
			want: baseConfig,
		},
		{
			name: "--path にルートを明示",
			args: []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--path", ""},
			want: baseConfig,
		},
		{
			name: "--path に空文字列キー(ルートとは別物)",
			args: []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--path", "/"},
			want: func(t *testing.T) *Config {
				c := baseConfig(t)
				c.Path = cliPointer(t, "/")
				return c
			},
		},
		{
			name: "--path にネストした位置",
			args: []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--path", "/data/items"},
			want: func(t *testing.T) *Config {
				c := baseConfig(t)
				c.Path = cliPointer(t, "/data/items")
				return c
			},
		},
		{
			name: "--append",
			args: []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--append"},
			want: func(t *testing.T) *Config {
				c := baseConfig(t)
				c.Append = true
				return c
			},
		},
		{
			name: "--overwrite",
			args: []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--overwrite"},
			want: func(t *testing.T) *Config {
				c := baseConfig(t)
				c.Overwrite = true
				return c
			},
		},
		{
			name: "--newline lf(既定と同じ)",
			args: []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--newline", "lf"},
			want: baseConfig,
		},
		{
			name: "--newline crlf",
			args: []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--newline", "crlf"},
			want: func(t *testing.T) *Config {
				c := baseConfig(t)
				c.Newline = "\r\n"
				return c
			},
		},
		{
			name: "--no-trailing-newline",
			args: []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--no-trailing-newline"},
			want: func(t *testing.T) *Config {
				c := baseConfig(t)
				c.TrailingNewline = false
				return c
			},
		},
		{
			name: "--cursor-key",
			args: []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--cursor-key", "/id"},
			want: func(t *testing.T) *Config {
				c := baseConfig(t)
				c.CursorKey = cliPointerRef(t, "/id")
				return c
			},
		},
		{
			name: "--dedupe-key",
			args: []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--dedupe-key", "/key"},
			want: func(t *testing.T) *Config {
				c := baseConfig(t)
				c.DedupeKey = cliPointerRef(t, "/key")
				return c
			},
		},
		{
			name: "--key-hyphen-to-underscore",
			args: []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--key-hyphen-to-underscore"},
			want: func(t *testing.T) *Config {
				c := baseConfig(t)
				c.HyphenToUnder = true
				return c
			},
		},
		{
			name: "--add-field 1 個",
			args: []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--add-field", "src=api"},
			want: func(t *testing.T) *Config {
				c := baseConfig(t)
				c.AddFields = []AddField{{Key: "src", Value: "api"}}
				return c
			},
		},
		{
			name: "--add-field 複数(出現順を保持、要件 6.3)",
			args: []string{
				"--in", "/in/a.json", "--out", "/out/o.ndjson",
				"--add-field", "z=1", "--add-field", "a=2", "--add-field", "m=3",
			},
			want: func(t *testing.T) *Config {
				c := baseConfig(t)
				c.AddFields = []AddField{{Key: "z", Value: "1"}, {Key: "a", Value: "2"}, {Key: "m", Value: "3"}}
				return c
			},
		},
		{
			name: "--add-field の値は空でもよい",
			args: []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--add-field", "src="},
			want: func(t *testing.T) *Config {
				c := baseConfig(t)
				c.AddFields = []AddField{{Key: "src", Value: ""}}
				return c
			},
		},
		{
			name: "--add-field の値に = を含められる(最初の = で分割)",
			args: []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--add-field", "a=b=c"},
			want: func(t *testing.T) *Config {
				c := baseConfig(t)
				c.AddFields = []AddField{{Key: "a", Value: "b=c"}}
				return c
			},
		},
		{
			name: "--max-record-bytes",
			args: []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--max-record-bytes", "1024"},
			want: func(t *testing.T) *Config {
				c := baseConfig(t)
				c.MaxRecordBytes = 1024
				return c
			},
		},
		{
			name: "--max-record-bytes + --skip-oversize",
			args: []string{
				"--in", "/in/a.json", "--out", "/out/o.ndjson",
				"--max-record-bytes", "1024", "--skip-oversize",
			},
			want: func(t *testing.T) *Config {
				c := baseConfig(t)
				c.MaxRecordBytes = 1024
				c.SkipOversize = true
				return c
			},
		},
		{
			name: "--in 複数(ベース名の昇順に整列、要件 1.2)",
			args: []string{"--in", "/in/b.json", "--in", "/in/a.json", "--out", "/out/o.ndjson"},
			want: func(t *testing.T) *Config {
				c := baseConfig(t)
				c.Inputs = []string{"/in/a.json", "/in/b.json"}
				return c
			},
		},
		{
			name: "空白と日本語を含むパス(要件 4.1)",
			args: []string{"--in", "/入力 ディレクトリ/デ ータ.json", "--out", "/出力 先/結果 ファイル.ndjson"},
			want: func(t *testing.T) *Config {
				c := baseConfig(t)
				c.Inputs = []string{"/入力 ディレクトリ/デ ータ.json"}
				c.Out = "/出力 先/結果 ファイル.ndjson"
				return c
			},
		},
		{
			name: "= 記法(--in=... 形式)",
			args: []string{"--in=/in/a.json", "--out=/out/o.ndjson"},
			want: baseConfig,
		},
		{
			name: "単一ハイフン形式(flag パッケージの標準挙動)",
			args: []string{"-in", "/in/a.json", "-out", "/out/o.ndjson"},
			want: baseConfig,
		},
		{
			name: "全オプション同時指定(--append 側)",
			args: []string{
				"--in", "/in/b.json", "--in", "/in/a.json",
				"--out", "/out/o.ndjson",
				"--path", "/data/items",
				"--append",
				"--newline", "crlf",
				"--no-trailing-newline",
				"--cursor-key", "/id",
				"--dedupe-key", "/key",
				"--key-hyphen-to-underscore",
				"--add-field", "src=api", "--add-field", "run=1",
				"--max-record-bytes", "65536",
				"--skip-oversize",
			},
			want: func(t *testing.T) *Config {
				return &Config{
					Inputs:          []string{"/in/a.json", "/in/b.json"},
					Out:             "/out/o.ndjson",
					Path:            cliPointer(t, "/data/items"),
					Append:          true,
					Newline:         "\r\n",
					TrailingNewline: false,
					CursorKey:       cliPointerRef(t, "/id"),
					DedupeKey:       cliPointerRef(t, "/key"),
					HyphenToUnder:   true,
					AddFields:       []AddField{{Key: "src", Value: "api"}, {Key: "run", Value: "1"}},
					MaxRecordBytes:  65536,
					SkipOversize:    true,
				}
			},
		},
		{
			name: "全オプション同時指定(--overwrite 側)",
			args: []string{
				"--in", "/in/a.json",
				"--out", "/out/o.ndjson",
				"--path", "/data",
				"--overwrite",
				"--newline", "lf",
				"--cursor-key", "/meta/next",
				"--dedupe-key", "/id",
				"--key-hyphen-to-underscore",
				"--add-field", "k=v",
				"--max-record-bytes", "1",
				"--skip-oversize",
			},
			want: func(t *testing.T) *Config {
				return &Config{
					Inputs:          []string{"/in/a.json"},
					Out:             "/out/o.ndjson",
					Path:            cliPointer(t, "/data"),
					Overwrite:       true,
					Newline:         "\n",
					TrailingNewline: true,
					CursorKey:       cliPointerRef(t, "/meta/next"),
					DedupeKey:       cliPointerRef(t, "/id"),
					HyphenToUnder:   true,
					AddFields:       []AddField{{Key: "k", Value: "v"}},
					MaxRecordBytes:  1,
					SkipOversize:    true,
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseArgs(tt.args)
			if err != nil {
				t.Fatalf("parseArgs(%q) = エラー %v, want 成功", tt.args, err)
			}
			want := tt.want(t)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("parseArgs(%q) の Config が一致しません:\n got = %+v\nwant = %+v", tt.args, got, want)
			}
		})
	}
}

// TestParseArgsDefaults は既定値を個別に固定する。
// マトリクスの DeepEqual だけだと期待値の書き換えで一緒にすり抜けるため、
// 要件 3.3(既定 LF)・3.5(既定で最終行改行あり)を独立に主張する。
func TestParseArgsDefaults(t *testing.T) {
	cfg, err := parseArgs([]string{"--in", "/in/a.json", "--out", "/out/o.ndjson"})
	if err != nil {
		t.Fatalf("parseArgs が失敗しました: %v", err)
	}
	if cfg.Newline != "\n" {
		t.Errorf("既定の Newline = %q, want %q(要件 3.3)", cfg.Newline, "\n")
	}
	if !cfg.TrailingNewline {
		t.Error("既定の TrailingNewline = false, want true(要件 3.5: 既定では最終行にも改行を付ける)")
	}
	if len(cfg.Path) != 0 {
		t.Errorf("既定の Path = %v, want ルート(長さ 0)(要件 2.1)", cfg.Path)
	}
	if cfg.CursorKey != nil {
		t.Errorf("既定の CursorKey = %v, want nil", cfg.CursorKey)
	}
	if cfg.DedupeKey != nil {
		t.Errorf("既定の DedupeKey = %v, want nil", cfg.DedupeKey)
	}
	if cfg.MaxRecordBytes != 0 {
		t.Errorf("既定の MaxRecordBytes = %d, want 0(無制限)", cfg.MaxRecordBytes)
	}
	if cfg.Append || cfg.Overwrite || cfg.HyphenToUnder || cfg.SkipOversize {
		t.Errorf("既定の真偽値オプションが false ではありません: %+v", cfg)
	}
	if len(cfg.AddFields) != 0 {
		t.Errorf("既定の AddFields = %v, want 空", cfg.AddFields)
	}
}

// TestParseArgsTrailingNewlinePolarity は --no-trailing-newline の極性を両方向で固定する
// (要件 3.5 / FR-14)。極性を反転する変異はこのテストで必ず落ちる。
func TestParseArgsTrailingNewlinePolarity(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"未指定なら最終行に改行を付ける", []string{"--in", "/a.json", "--out", "/o"}, true},
		{"指定すると最終行に改行を付けない", []string{"--in", "/a.json", "--out", "/o", "--no-trailing-newline"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := parseArgs(tt.args)
			if err != nil {
				t.Fatalf("parseArgs(%q) が失敗しました: %v", tt.args, err)
			}
			if cfg.TrailingNewline != tt.want {
				t.Errorf("TrailingNewline = %v, want %v", cfg.TrailingNewline, tt.want)
			}
		})
	}
}

// TestParseArgsSortsInputs は複数入力の整列規則(要件 1.2 / FR-02)を検証する。
// 整列はここでしか行われない(convert は再整列しない)ため、出力順序の保証はこの 1 点に依存する。
func TestParseArgsSortsInputs(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "同一ディレクトリのベース名昇順",
			in:   []string{"/in/c.json", "/in/a.json", "/in/b.json"},
			want: []string{"/in/a.json", "/in/b.json", "/in/c.json"},
		},
		{
			name: "整列済みの入力は変わらない",
			in:   []string{"/in/a.json", "/in/b.json"},
			want: []string{"/in/a.json", "/in/b.json"},
		},
		{
			name: "単一入力",
			in:   []string{"/in/only.json"},
			want: []string{"/in/only.json"},
		},
		{
			name: "ディレクトリが違ってもベース名で並ぶ(フルパス順ではない)",
			in:   []string{"/z/a.json", "/a/b.json"},
			want: []string{"/z/a.json", "/a/b.json"},
		},
		{
			name: "ベース名が同一ならフルパスの昇順",
			in:   []string{"/z/page.json", "/a/page.json", "/m/page.json"},
			want: []string{"/a/page.json", "/m/page.json", "/z/page.json"},
		},
		{
			name: "ベース名同一と相異が混在",
			in:   []string{"/z/page.json", "/in/a.json", "/a/page.json"},
			want: []string{"/in/a.json", "/a/page.json", "/z/page.json"},
		},
		{
			name: "ゼロ埋めされた連番",
			in:   []string{"/in/page-010.json", "/in/page-002.json", "/in/page-001.json"},
			want: []string{"/in/page-001.json", "/in/page-002.json", "/in/page-010.json"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := []string{"--out", "/out/o.ndjson"}
			for _, in := range tt.in {
				args = append(args, "--in", in)
			}
			cfg, err := parseArgs(args)
			if err != nil {
				t.Fatalf("parseArgs(%q) が失敗しました: %v", args, err)
			}
			if !reflect.DeepEqual(cfg.Inputs, tt.want) {
				t.Errorf("Inputs = %q, want %q", cfg.Inputs, tt.want)
			}
		})
	}
}

// TestParseArgsUsageErrors は異常系マトリクス。いずれも終了コード 1(要件 7.3 / FR-37)で、
// メッセージは日本語かつ問題のある引数名を含まなければならない(要件 8.2)。
func TestParseArgsUsageErrors(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantMsgs []string // すべてがメッセージに含まれること
	}{
		{
			name:     "引数なし",
			args:     nil,
			wantMsgs: []string{"--in"},
		},
		{
			name:     "--in の欠落",
			args:     []string{"--out", "/out/o.ndjson"},
			wantMsgs: []string{"--in", "必須"},
		},
		{
			name:     "--out の欠落",
			args:     []string{"--in", "/in/a.json"},
			wantMsgs: []string{"--out", "必須"},
		},
		{
			name:     "--in が空文字列",
			args:     []string{"--in", "", "--out", "/out/o.ndjson"},
			wantMsgs: []string{"--in", "空"},
		},
		{
			name:     "--out が空文字列",
			args:     []string{"--in", "/in/a.json", "--out", ""},
			wantMsgs: []string{"--out", "空"},
		},
		{
			name:     "未知の引数",
			args:     []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--unknown-flag"},
			wantMsgs: []string{"unknown-flag"},
		},
		{
			name:     "余分な位置引数",
			args:     []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "extra.json"},
			wantMsgs: []string{"extra.json"},
		},
		{
			name:     "--append と --overwrite の同時指定",
			args:     []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--append", "--overwrite"},
			wantMsgs: []string{"--append", "--overwrite"},
		},
		{
			name:     "--newline の値域外",
			args:     []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--newline", "cr"},
			wantMsgs: []string{"--newline", "lf", "crlf"},
		},
		{
			name:     "--newline の大文字表記は受け付けない",
			args:     []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--newline", "LF"},
			wantMsgs: []string{"--newline"},
		},
		{
			name:     "--newline に空文字列",
			args:     []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--newline", ""},
			wantMsgs: []string{"--newline"},
		},
		{
			name:     "--path が / で始まらない",
			args:     []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--path", "data"},
			wantMsgs: []string{"--path"},
		},
		{
			name:     "--path の ~ が宙に浮いている",
			args:     []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--path", "/a~"},
			wantMsgs: []string{"--path"},
		},
		{
			name:     "--path の未定義エスケープ",
			args:     []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--path", "/a~2b"},
			wantMsgs: []string{"--path"},
		},
		{
			name:     "--cursor-key の ~ が宙に浮いている",
			args:     []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--cursor-key", "/id~"},
			wantMsgs: []string{"--cursor-key"},
		},
		{
			name:     "--cursor-key が / で始まらない",
			args:     []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--cursor-key", "id"},
			wantMsgs: []string{"--cursor-key"},
		},
		{
			name:     "--dedupe-key の ~ が宙に浮いている",
			args:     []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--dedupe-key", "~"},
			wantMsgs: []string{"--dedupe-key"},
		},
		{
			name:     "--dedupe-key が / で始まらない",
			args:     []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--dedupe-key", "key"},
			wantMsgs: []string{"--dedupe-key"},
		},
		{
			name:     "--add-field に = がない",
			args:     []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--add-field", "novalue"},
			wantMsgs: []string{"--add-field", "novalue"},
		},
		{
			name:     "--add-field のキーが空",
			args:     []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--add-field", "=v"},
			wantMsgs: []string{"--add-field", "キー"},
		},
		{
			name: "--add-field のキー重複",
			args: []string{
				"--in", "/in/a.json", "--out", "/out/o.ndjson",
				"--add-field", "k=1", "--add-field", "k=2",
			},
			wantMsgs: []string{"--add-field", "k"},
		},
		{
			name:     "--max-record-bytes が 0",
			args:     []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--max-record-bytes", "0"},
			wantMsgs: []string{"--max-record-bytes", "正の整数"},
		},
		{
			name:     "--max-record-bytes が負",
			args:     []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--max-record-bytes", "-1"},
			wantMsgs: []string{"--max-record-bytes"},
		},
		{
			name:     "--max-record-bytes が数値でない",
			args:     []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--max-record-bytes", "abc"},
			wantMsgs: []string{"max-record-bytes"},
		},
		{
			name:     "--skip-oversize の単独指定",
			args:     []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--skip-oversize"},
			wantMsgs: []string{"--skip-oversize", "--max-record-bytes"},
		},
		{
			name:     "--in に値がない",
			args:     []string{"--in"},
			wantMsgs: []string{"in"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := parseArgs(tt.args)
			if err == nil {
				t.Fatalf("parseArgs(%q) = %+v, want エラー", tt.args, cfg)
			}
			if cfg != nil {
				t.Errorf("parseArgs(%q) はエラー時に Config を返してはいけません: %+v", tt.args, cfg)
			}
			var exitErr *ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("parseArgs(%q) のエラー %v は *ExitError ではありません", tt.args, err)
			}
			if exitErr.Code != ExitUsage {
				t.Errorf("終了コード = %d, want %d(要件 7.3)", exitErr.Code, ExitUsage)
			}
			msg := err.Error()
			for _, want := range tt.wantMsgs {
				if !strings.Contains(msg, want) {
					t.Errorf("エラーメッセージに %q が含まれていません: %s", want, msg)
				}
			}
			if !containsJapanese(msg) {
				t.Errorf("エラーメッセージが日本語を含んでいません(要件 8.2): %s", msg)
			}
		})
	}
}

// containsJapanese はメッセージに日本語(ひらがな・カタカナ・漢字)が含まれるかを判定する。
func containsJapanese(s string) bool {
	for _, r := range s {
		switch {
		case r >= 0x3040 && r <= 0x30ff, // ひらがな・カタカナ
			r >= 0x4e00 && r <= 0x9fff: // CJK 統合漢字
			return true
		}
	}
	return false
}

// TestParseArgsShowVersion は --version の契約(要件 7.5)。
// parseArgs は表示せず番兵エラーを返し、表示と終了コード 0 は main の責務。
func TestParseArgsShowVersion(t *testing.T) {
	tests := [][]string{
		{"--version"},
		{"-version"},
		{"--version", "--in", "/in/a.json"}, // 必須引数の検証より優先される
		{"--version", "--help"},             // --version が優先
	}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var cfg *Config
			var err error
			stdout, stderr := captureStdio(t, func() {
				cfg, err = parseArgs(args)
			})
			if !errors.Is(err, errShowVersion) {
				t.Fatalf("parseArgs(%q) = %v, want errShowVersion", args, err)
			}
			if cfg != nil {
				t.Errorf("--version は Config を返してはいけません: %+v", cfg)
			}
			if stdout != "" {
				t.Errorf("parseArgs が stdout へ書き込みました: %q(表示は main の責務)", stdout)
			}
			if stderr != "" {
				t.Errorf("parseArgs が stderr へ書き込みました: %q(表示は main の責務)", stderr)
			}
		})
	}
}

// TestParseArgsShowHelp は --help の契約(要件 7.6)。
func TestParseArgsShowHelp(t *testing.T) {
	tests := [][]string{
		{"--help"},
		{"-help"},
		{"-h"},
		{"--help", "--in", "/in/a.json"}, // 必須引数の検証より優先される
	}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var cfg *Config
			var err error
			stdout, stderr := captureStdio(t, func() {
				cfg, err = parseArgs(args)
			})
			if !errors.Is(err, errShowHelp) {
				t.Fatalf("parseArgs(%q) = %v, want errShowHelp", args, err)
			}
			if cfg != nil {
				t.Errorf("--help は Config を返してはいけません: %+v", cfg)
			}
			if stdout != "" {
				t.Errorf("parseArgs が stdout へ書き込みました: %q(表示は main の責務)", stdout)
			}
			if stderr != "" {
				t.Errorf("parseArgs が stderr へ書き込みました: %q(表示は main の責務)", stderr)
			}
		})
	}
}

// TestParseArgsShowVersionAndHelpAreDistinct は 2 つの番兵が取り違えられないことを固定する。
func TestParseArgsShowVersionAndHelpAreDistinct(t *testing.T) {
	if errors.Is(errShowVersion, errShowHelp) || errors.Is(errShowHelp, errShowVersion) {
		t.Fatal("errShowVersion と errShowHelp が同一視されています")
	}
	if _, err := parseArgs([]string{"--help"}); errors.Is(err, errShowVersion) {
		t.Error("--help が errShowVersion と一致しました")
	}
	if _, err := parseArgs([]string{"--version"}); errors.Is(err, errShowHelp) {
		t.Error("--version が errShowHelp と一致しました")
	}
}

// TestParseArgsSentinelsAreNotExitErrors は番兵が終了コード 1 に写像されないことを検証する。
// main が errors.As(*ExitError) を先に評価しても 0 で終われるように、番兵は ExitError であってはならない。
func TestParseArgsSentinelsAreNotExitErrors(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"--help"}} {
		_, err := parseArgs(args)
		var exitErr *ExitError
		if errors.As(err, &exitErr) {
			t.Errorf("parseArgs(%q) のエラーが *ExitError(code=%d)です。終了コード 0 で終われません(要件 7.5/7.6)",
				args, exitErr.Code)
		}
	}
}

// TestParseArgsWritesNothingToStdio は解析が標準出力・標準エラー出力を汚さないことを検証する
// (要件 8.1、design.md「flag の既定エラー出力は stderr に限定し、stdout を汚さない」)。
// flag パッケージは既定で使い方を stderr へ出し os.Exit(2) するため、抑止できていないと
// このテストはプロセスごと落ちるか出力が残る。
func TestParseArgsWritesNothingToStdio(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"未知の引数", []string{"--bogus"}},
		{"値のない引数", []string{"--in"}},
		{"型不一致の値", []string{"--in", "/a.json", "--out", "/o", "--max-record-bytes", "abc"}},
		{"検証エラー", []string{"--in", "/a.json", "--out", "/o", "--append", "--overwrite"}},
		{"正常系", []string{"--in", "/a.json", "--out", "/o"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr := captureStdio(t, func() {
				_, _ = parseArgs(tt.args)
			})
			if stdout != "" {
				t.Errorf("stdout に %q が書かれました(要件 8.1: 標準出力は結果行のみ)", stdout)
			}
			if stderr != "" {
				t.Errorf("stderr に %q が書かれました(出力先の決定は main の責務)", stderr)
			}
		})
	}
}

// TestParseArgsIgnoresEnvironment は設定が引数だけで決まることを検証する(要件 7.1 / FR-35)。
func TestParseArgsIgnoresEnvironment(t *testing.T) {
	for _, key := range []string{"JSON2NDJSON_IN", "JSON2NDJSON_OUT", "IN", "OUT", "J2N_OUT"} {
		t.Setenv(key, "/env/should-not-be-used")
	}
	if _, err := parseArgs([]string{"--in", "/in/a.json"}); err == nil {
		t.Error("環境変数から --out を補ってしまいました(要件 7.1: 暗黙の設定を持たない)")
	}
	cfg, err := parseArgs([]string{"--in", "/in/a.json", "--out", "/out/o.ndjson"})
	if err != nil {
		t.Fatalf("parseArgs が失敗しました: %v", err)
	}
	if cfg.Out != "/out/o.ndjson" {
		t.Errorf("Out = %q, want %q", cfg.Out, "/out/o.ndjson")
	}
}

// TestParseArgsDoesNotReadStdin は解析が対話的な入力待ちをしないことを検証する
// (要件 7.2 / FR-36)。標準入力を「決して書き込まれないパイプ」に差し替え、
// 読もうとすればブロックして期限切れになる状況を作る。
func TestParseArgsDoesNotReadStdin(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe に失敗しました: %v", err)
	}
	defer func() {
		_ = w.Close()
		_ = r.Close()
	}()

	orig := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = orig }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = parseArgs([]string{"--in", "/in/a.json", "--out", "/out/o.ndjson"})
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("parseArgs が標準入力の待ちで停止しました(要件 7.2: 対話的な入力待ちを行わない)")
	}
}

// TestUsageTextCoversAllFlags は --help が全引数を説明することを検証する。
// 本ツールはログファイルを持たない(要件 11.2)ため、ヘルプが運用者の唯一の文書となる。
func TestUsageTextCoversAllFlags(t *testing.T) {
	usage := usageText()
	flags := []string{
		"--in", "--out", "--path", "--append", "--overwrite", "--newline",
		"--no-trailing-newline", "--cursor-key", "--dedupe-key",
		"--key-hyphen-to-underscore", "--add-field", "--max-record-bytes",
		"--skip-oversize", "--version", "--help",
	}
	for _, f := range flags {
		if !strings.Contains(usage, f) {
			t.Errorf("--help の出力に %s の説明がありません", f)
		}
	}
	for _, want := range []string{"json2ndjson", "lf", "crlf"} {
		if !strings.Contains(usage, want) {
			t.Errorf("--help の出力に %q が含まれていません", want)
		}
	}
	if !containsJapanese(usage) {
		t.Error("--help の出力が日本語ではありません")
	}
}

// TestUsageTextContainsDedupeMemoryNote は要件 9.4 / NFR-05 の配線を検証する。
// 重複排除のキー集合がメモリ上限に含まれ大規模キーで超過しうる旨は、
// transform.go の dedupeMemoryNote に確定済みで、ヘルプへの埋め込みが本タスクの責務。
func TestUsageTextContainsDedupeMemoryNote(t *testing.T) {
	usage := usageText()
	if !strings.Contains(usage, dedupeMemoryNote) {
		t.Errorf("--help の出力に dedupeMemoryNote が含まれていません(要件 9.4)\nusage:\n%s", usage)
	}
}

// TestUsageTextDocumentsDedupeMissingPosition は「--dedupe-key の位置が存在しない
// レコードは重複排除の対象外として常に出力される」旨(タスク 2.2 が確定した挙動)の記載を検証する。
func TestUsageTextDocumentsDedupeMissingPosition(t *testing.T) {
	usage := usageText()
	for _, want := range []string{"存在しない", "常に出力"} {
		if !strings.Contains(usage, want) {
			t.Errorf("--help に --dedupe-key の位置が存在しないレコードの扱いの記載(%q)がありません", want)
		}
	}
}

// TestUsageTextIsStable は usageText が呼び出しごとに同じ文字列を返すことを検証する
// (flag パッケージの内部状態に依存しないこと)。
func TestUsageTextIsStable(t *testing.T) {
	first := usageText()
	if first == "" {
		t.Fatal("usageText が空文字列を返しました")
	}
	if second := usageText(); first != second {
		t.Errorf("usageText の出力が安定していません:\n1 回目:\n%s\n2 回目:\n%s", first, second)
	}
}

// TestParseArgsDoesNotShareState は連続した呼び出しが状態を共有しないことを検証する。
// --in / --add-field を flag.Func で受けるため、蓄積先をパッケージ変数にすると前回の値が残る。
func TestParseArgsDoesNotShareState(t *testing.T) {
	args := []string{"--in", "/in/a.json", "--out", "/out/o.ndjson", "--add-field", "k=v"}
	first, err := parseArgs(args)
	if err != nil {
		t.Fatalf("1 回目の parseArgs が失敗しました: %v", err)
	}
	second, err := parseArgs(args)
	if err != nil {
		t.Fatalf("2 回目の parseArgs が失敗しました: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("同じ引数で結果が変わりました:\n1 回目 = %+v\n2 回目 = %+v", first, second)
	}
	if len(second.Inputs) != 1 || len(second.AddFields) != 1 {
		t.Errorf("2 回目の呼び出しに前回の値が残っています: Inputs=%q AddFields=%v", second.Inputs, second.AddFields)
	}
}
