package main

import (
	"strings"
	"testing"
)

// TestVersionLineFormat は --version が表示する 1 行の形式を固定する
// (design.md「errors / version」: `json2ndjson <version>`、要件 7.5 / NFR-11)。
func TestVersionLineFormat(t *testing.T) {
	orig := version
	t.Cleanup(func() { version = orig })

	tests := []struct {
		name    string
		version string
		want    string
	}{
		{"未注入(既定)", "dev", "json2ndjson dev"},
		{"タグ注入", "v1.2.3", "json2ndjson v1.2.3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			version = tt.version
			if got := versionLine(); got != tt.want {
				t.Errorf("versionLine() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestVersionDefaultIsDev はビルド時に -ldflags で上書きされなかった場合の既定値を固定する
// (IMPL-21)。テストは version を書き換えるため、既定値の検査は書き換え前の値で行う。
func TestVersionDefaultIsDev(t *testing.T) {
	if version != "dev" {
		t.Errorf("version の既定値 = %q, want %q(-ldflags 未注入時)", version, "dev")
	}
}

// TestVersionLineIsSingleLine は結果行と混同されないよう改行を含まないことを検証する。
func TestVersionLineIsSingleLine(t *testing.T) {
	if strings.ContainsAny(versionLine(), "\n\r") {
		t.Errorf("versionLine() が改行を含んでいます: %q", versionLine())
	}
}
