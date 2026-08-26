package main

import (
	"fmt"
	"strconv"
	"strings"
)

// Pointer は RFC 6901 JSON Pointer の参照トークン列(エスケープ解除済み)。
// 空スライスはルートを指す(design.md「pointer(RFC 6901 JSON Pointer)」)。
type Pointer []string

// ParsePointer は RFC 6901 の JSON Pointer 文字列を参照トークン列へ解析する(要件 2.1)。
//
// 規則:
//   - 空文字列はルートを指し、トークンを 1 つも持たない Pointer を返す。
//   - 空文字列でないポインタは "/" で始まらなければならない。
//   - "/" で分割した各断片が参照トークンで、"/" 単独はトークン 1 個(空文字列キー)を表す。
//     ルート(長さ 0)と空文字列キー(長さ 1)は別物である。
//   - エスケープは "~1"→"/"、"~0"→"~" の 2 つだけで、それ以外の "~" の使い方
//     ("~" で終わる、"~2" など)は不正形式としてエラーを返す。
//
// 本関数の責務は解析のみであり、JSON 文書への適用(移動)は convert / transform が行う。
// 解析は入力文字列のみに依存し副作用を持たない。返す Pointer は呼び出しごとに
// 新しく確保するため、呼び出し側が書き換えても後続の解析に影響しない。
//
// 不正形式は素の error として返す。終了コード(1)への写像は呼び出し側(cli)の責務のため、
// ここでは ExitError を用いない(design.md「Error Handling」)。
func ParsePointer(s string) (Pointer, error) {
	if s == "" {
		return Pointer{}, nil
	}
	rest, ok := strings.CutPrefix(s, "/")
	if !ok {
		return nil, fmt.Errorf("JSON Pointer %q が %q で始まっていません", s, "/")
	}

	// CutPrefix 後の文字列を "/" で分割する。"/" 単独なら rest は空文字列となり、
	// 空文字列トークンが 1 個得られる(ルートとの区別)。
	p := Pointer{}
	for raw := range strings.SplitSeq(rest, "/") {
		token, err := unescapeReferenceToken(raw)
		if err != nil {
			return nil, fmt.Errorf("JSON Pointer %q の参照トークン %q が不正です: %w", s, raw, err)
		}
		p = append(p, token)
	}
	return p, nil
}

// unescapeReferenceToken は参照トークンのエスケープを解除する(RFC 6901 §4)。
//
// 左から右へ 1 度だけ走査し、"~0" と "~1" をその場で置き換える。
// 単純な文字列置換を 2 回続ける実装(例: "~0"→"~" の後に "~1"→"/")は
// "~01" を "~/" にしてしまうため採らない。1 パス走査であれば "~01" は
// "~0"→"~" を出力した後に残りの "1" をそのまま出力し、正しく "~1" となる。
func unescapeReferenceToken(token string) (string, error) {
	i := strings.IndexByte(token, '~')
	if i < 0 {
		// エスケープを含まないトークンが大多数のため、確保せずそのまま返す。
		return token, nil
	}

	var b strings.Builder
	b.Grow(len(token))
	b.WriteString(token[:i])
	for i < len(token) {
		c := token[i]
		if c != '~' {
			b.WriteByte(c)
			i++
			continue
		}
		if i+1 >= len(token) {
			return "", fmt.Errorf("%q が単独で現れています(%q または %q のみ有効です)", "~", "~0", "~1")
		}
		switch token[i+1] {
		case '0':
			b.WriteByte('~')
		case '1':
			b.WriteByte('/')
		default:
			return "", fmt.Errorf("未定義のエスケープ %q です(%q または %q のみ有効です)", token[i:i+2], "~0", "~1")
		}
		i += 2
	}
	return b.String(), nil
}

// parseArrayIndex は参照トークンを配列添字として解釈し、値と可否を返す(RFC 6901 §4)。
//
// 添字として有効なのは "0" または [1-9][0-9]* のみで、先頭ゼロ("01"、"007")、
// 符号付き("-1"、"+1")、小数("1.0")、空文字列、および配列末尾を指す "-" は
// 添字ではない。int で表現できない桁数の値も false を返す(実在する配列の要素数を
// 必ず超えるため、添字として扱う意味がない)。
//
// ParsePointer がこの規則をポインタ全体の検証に用いないのは、参照トークンが
// 添字であることを要求されるのは、それが指す値が配列だと分かった時点に限られるため。
// ParsePointer は文書を持たないので判断できず、"01" はオブジェクトキーとしては
// 完全に有効である。したがって添字規則は本関数として切り出し、配列に遭遇した
// convert / transform が呼び出す。
func parseArrayIndex(token string) (int, bool) {
	if token == "" {
		return 0, false
	}
	if token == "0" {
		return 0, true
	}
	// 先頭は 1〜9(先頭ゼロ禁止)、以降は 0〜9 のみ。
	if token[0] < '1' || token[0] > '9' {
		return 0, false
	}
	for i := 1; i < len(token); i++ {
		if token[i] < '0' || token[i] > '9' {
			return 0, false
		}
	}
	// 字句は検証済みのため、ここでのエラーは int の範囲超過だけ。
	n, err := strconv.Atoi(token)
	if err != nil {
		return 0, false
	}
	return n, true
}
