package main

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"strings"
	"testing"
)

// exitCodeOf は err から ExitError の終了コードを取り出す。
// ExitError でない場合はテストを失敗させる(終了コード体系は要件 7.4 の外部契約であり、
// 「エラーになった」ことだけでは検証として不十分なため)。
func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		t.Fatal("エラーが返されませんでした")
	}
	var ee *ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("ExitError ではないエラーが返されました: %#v", err)
	}
	return ee.Code
}

// mustPointer はテスト内で JSON Pointer 文字列を解析する。
func mustPointer(t *testing.T, s string) *Pointer {
	t.Helper()
	p, err := ParsePointer(s)
	if err != nil {
		t.Fatalf("ParsePointer(%q) が失敗しました: %v", s, err)
	}
	return &p
}

// applyOne は 1 レコードだけを整形して結果を返す(単独機能のテスト用)。
func applyOne(t *testing.T, cfg *Config, rec string) (string, bool, error) {
	t.Helper()
	p := newPipeline(cfg)
	out, skip, err := p.Apply(jsontext.Value(rec))
	return string(out), skip, err
}

// --- (1) キー名置換の単独テスト(要件 6.2 / FR-33)--------------------------------

func TestPipelineApplyRenameOnly(t *testing.T) {
	tests := []struct {
		name string
		rec  string
		want string
	}{
		{
			name: "トップレベルのキーの - が _ になる",
			rec:  `{"a-b":1}`,
			want: `{"a_b":1}`,
		},
		{
			name: "すべての階層(ネストしたオブジェクト・配列内のオブジェクト)に適用される",
			rec:  `{"a-b":[{"c-d":1},{"e-f":[{"g-h":2}]}],"i":{"j-k":{"l-m":3}}}`,
			want: `{"a_b":[{"c_d":1},{"e_f":[{"g_h":2}]}],"i":{"j_k":{"l_m":3}}}`,
		},
		{
			name: "キー名が - のみでも置換される",
			rec:  `{"-":1}`,
			want: `{"_":1}`,
		},
		{
			name: "複数の - をすべて置換する",
			rec:  `{"i--j-k":1,"--":2}`,
			want: `{"i__j_k":1,"__":2}`,
		},
		{
			// キー名の - は \u002d と書いてもよい(RFC 8259)。意味上のハイフンなので置換対象。
			name: `\u002d と書かれたキーのハイフンも置換される`,
			rec:  "{\"a\\u002db\":1,\"c\\u002Dd\":2,\"\\u002d\":3}",
			want: `{"a_b":1,"c_d":2,"_":3}`,
		},
		{
			name: "値の中の - は置換しない(キー名だけが対象)",
			rec:  `{"k":"a-b","n":[ "x-y" ]}`,
			want: `{"k":"a-b","n":["x-y"]}`,
		},
		{
			name: "トップレベルが配列でもネストしたキーを置換する",
			rec:  `[{"x-y":1},{"z":2}]`,
			want: `[{"x_y":1},{"z":2}]`,
		},
		{
			name: "トップレベルがスカラーなら置換対象がなく素通しする",
			rec:  `"a-b"`,
			want: `"a-b"`,
		},
		{
			name: "空オブジェクト・空配列",
			rec:  `{"a-b":{},"c-d":[]}`,
			want: `{"a_b":{},"c_d":[]}`,
		},
		{
			// \\ の直後の u002d は「エスケープされたバックスラッシュ + 文字列 u002d」であり、
			// ハイフンのエスケープではない。誤って置換してはならない。
			name: `エスケープされたバックスラッシュに続く u002d を誤認しない`,
			rec:  `{"a\\u002db":1}`,
			want: `{"a\\u002db":1}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{HyphenToUnder: true}
			got, skip, err := applyOne(t, cfg, tt.rec)
			if err != nil {
				t.Fatalf("Apply が失敗しました: %v", err)
			}
			if skip {
				t.Fatal("skip=true になりました(スキップされるべきではありません)")
			}
			if got != tt.want {
				t.Errorf("出力が一致しません\n got: %s\nwant: %s", got, tt.want)
			}
		})
	}
}

// --- (2) 固定値追加の単独テスト(要件 6.3, 6.4 / FR-34)---------------------------

func TestPipelineApplyAddFieldOnly(t *testing.T) {
	tests := []struct {
		name     string
		adds     []AddField
		rec      string
		want     string
		wantCode int // 0 は成功期待
	}{
		{
			name: "トップレベルの末尾に追加する",
			adds: []AddField{{Key: "src", Value: "api"}},
			rec:  `{"a":1}`,
			want: `{"a":1,"src":"api"}`,
		},
		{
			name: "複数指定は指定順に追加される",
			adds: []AddField{{Key: "b", Value: "1"}, {Key: "c", Value: "2"}},
			rec:  `{"a":1}`,
			want: `{"a":1,"b":"1","c":"2"}`,
		},
		{
			name: "空オブジェクトにも追加できる",
			adds: []AddField{{Key: "b", Value: "x"}},
			rec:  `{}`,
			want: `{"b":"x"}`,
		},
		{
			name: "値は常に文字列として追加される",
			adds: []AddField{{Key: "n", Value: "123"}},
			rec:  `{"a":1}`,
			want: `{"a":1,"n":"123"}`,
		},
		{
			name: "エスケープが必要な値を正しく符号化する",
			adds: []AddField{{Key: "q", Value: `x"y\z`}},
			rec:  `{"a":1}`,
			want: `{"a":1,"q":"x\"y\\z"}`,
		},
		{
			name: "非 ASCII の値は UTF-8 のまま追加する",
			adds: []AddField{{Key: "j", Value: "あ"}},
			rec:  `{"a":1}`,
			want: `{"a":1,"j":"あ"}`,
		},
		{
			name:     "トップレベルの既存キーと衝突したら終了コード 1",
			adds:     []AddField{{Key: "a", Value: "x"}},
			rec:      `{"a":1}`,
			wantCode: ExitUsage,
		},
		{
			name: "ネストにのみ同名キーがある場合は衝突ではない",
			adds: []AddField{{Key: "b", Value: "x"}},
			rec:  `{"a":{"b":1}}`,
			want: `{"a":{"b":1},"b":"x"}`,
		},
		{
			name:     "レコードが配列なら終了コード 1",
			adds:     []AddField{{Key: "b", Value: "x"}},
			rec:      `[1,2]`,
			wantCode: ExitUsage,
		},
		{
			name:     "レコードがスカラーなら終了コード 1",
			adds:     []AddField{{Key: "b", Value: "x"}},
			rec:      `"scalar"`,
			wantCode: ExitUsage,
		},
		{
			// 既存キーが - で書かれていても、意味上のキー名で衝突を判定する。
			name:     `- で書かれた既存キーとも衝突を検出する`,
			adds:     []AddField{{Key: "a-b", Value: "x"}},
			rec:      "{\"a\\u002db\":1}",
			wantCode: ExitUsage,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{AddFields: tt.adds}
			got, skip, err := applyOne(t, cfg, tt.rec)
			if tt.wantCode != 0 {
				if code := exitCodeOf(t, err); code != tt.wantCode {
					t.Fatalf("終了コードが一致しません: got=%d want=%d (err=%v)", code, tt.wantCode, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Apply が失敗しました: %v", err)
			}
			if skip {
				t.Fatal("skip=true になりました(スキップされるべきではありません)")
			}
			if got != tt.want {
				t.Errorf("出力が一致しません\n got: %s\nwant: %s", got, tt.want)
			}
		})
	}
}

// --- (3) サイズ上限の単独テスト(要件 6.5, 6.6 / NFR-04)--------------------------

func TestPipelineApplyMaxRecordBytes(t *testing.T) {
	const rec = `{"a":1}` // 7 バイト

	tests := []struct {
		name     string
		max      int64
		skipOver bool
		wantSkip bool
		wantCode int
		wantWarn bool
	}{
		{name: "上限 0 は無制限", max: 0},
		{name: "上限より小さいレコードは通る", max: 8},
		{name: "上限と同一バイト数は通る(「超えた場合」のみ違反)", max: 7},
		{name: "上限を 1 バイト超えたら終了コード 5", max: 6, wantCode: ExitLocation},
		{name: "skip 指定なら skip=true・エラーなし・警告あり", max: 6, skipOver: true, wantSkip: true, wantWarn: true},
		{name: "skip 指定でも上限内なら警告を出さない", max: 7, skipOver: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{MaxRecordBytes: tt.max, SkipOversize: tt.skipOver}
			p := newPipeline(cfg)
			var warn bytes.Buffer
			p.SetWarnWriter(&warn)

			out, skip, err := p.Apply(jsontext.Value(rec))
			if tt.wantCode != 0 {
				if code := exitCodeOf(t, err); code != tt.wantCode {
					t.Fatalf("終了コードが一致しません: got=%d want=%d (err=%v)", code, tt.wantCode, err)
				}
				if warn.Len() != 0 {
					t.Errorf("skip 指定なしで警告が出ました: %q", warn.String())
				}
				return
			}
			if err != nil {
				t.Fatalf("Apply が失敗しました: %v", err)
			}
			if skip != tt.wantSkip {
				t.Fatalf("skip が一致しません: got=%v want=%v", skip, tt.wantSkip)
			}
			if !skip && string(out) != rec {
				t.Errorf("出力が一致しません\n got: %s\nwant: %s", out, rec)
			}
			if skip && out != nil {
				t.Errorf("skip 時に出力が返されました: %s", out)
			}
			gotWarn := warn.Len() > 0
			if gotWarn != tt.wantWarn {
				t.Fatalf("警告の有無が一致しません: got=%v want=%v (%q)", gotWarn, tt.wantWarn, warn.String())
			}
			if tt.wantWarn {
				if !strings.HasSuffix(warn.String(), "\n") {
					t.Errorf("警告が改行で終わっていません: %q", warn.String())
				}
				if !strings.Contains(warn.String(), "7") || !strings.Contains(warn.String(), "6") {
					t.Errorf("警告に実サイズと上限が含まれていません: %q", warn.String())
				}
			}
		})
	}
}

// 警告先を設定しなければ transform はどこへも書かない(convert/main の stderr 所有権を侵さない)。
func TestPipelineOversizeWarningIsOptional(t *testing.T) {
	cfg := &Config{MaxRecordBytes: 1, SkipOversize: true}
	p := newPipeline(cfg)
	out, skip, err := p.Apply(jsontext.Value(`{"a":1}`))
	if err != nil {
		t.Fatalf("警告先未設定で失敗しました: %v", err)
	}
	if !skip || out != nil {
		t.Fatalf("スキップされませんでした: skip=%v out=%s", skip, out)
	}
}

// --- (4) 重複排除の単独テスト(要件 6.1 / FR-32)----------------------------------

func TestPipelineApplyDedupeOnly(t *testing.T) {
	tests := []struct {
		name      string
		key       string
		recs      []string
		wantOut   []string // skip されたレコードは "" で表す
		wantSkips []bool
	}{
		{
			name:      "同一値の 2 件目以降をスキップする",
			key:       "/id",
			recs:      []string{`{"id":1}`, `{"id":2}`, `{"id":1}`},
			wantOut:   []string{`{"id":1}`, `{"id":2}`, ``},
			wantSkips: []bool{false, false, true},
		},
		{
			name:      "先に現れたレコードを残す(先勝ち)",
			key:       "/id",
			recs:      []string{`{"id":1,"v":"first"}`, `{"id":1,"v":"second"}`},
			wantOut:   []string{`{"id":1,"v":"first"}`, ``},
			wantSkips: []bool{false, true},
		},
		{
			name:      "異なる値はすべて出力する",
			key:       "/id",
			recs:      []string{`{"id":"a"}`, `{"id":"b"}`, `{"id":"c"}`},
			wantOut:   []string{`{"id":"a"}`, `{"id":"b"}`, `{"id":"c"}`},
			wantSkips: []bool{false, false, false},
		},
		{
			name:      "型が異なれば別の値として扱う(生字句のバイト列で比較)",
			key:       "/id",
			recs:      []string{`{"id":1}`, `{"id":"1"}`, `{"id":1.0}`},
			wantOut:   []string{`{"id":1}`, `{"id":"1"}`, `{"id":1.0}`},
			wantSkips: []bool{false, false, false},
		},
		{
			name:      "非スカラーの値でも重複排除できる",
			key:       "/id",
			recs:      []string{`{"id":{"a":1}}`, `{"id":{"a":1}}`, `{"id":{"a":2}}`},
			wantOut:   []string{`{"id":{"a":1}}`, ``, `{"id":{"a":2}}`},
			wantSkips: []bool{false, true, false},
		},
		{
			name:      "キーが存在しないレコードは重複排除の対象外として常に出力する",
			key:       "/id",
			recs:      []string{`{"x":1}`, `{"x":1}`, `{"id":1}`, `{"id":1}`},
			wantOut:   []string{`{"x":1}`, `{"x":1}`, `{"id":1}`, ``},
			wantSkips: []bool{false, false, false, true},
		},
		{
			name:      "ネストした位置のキーで重複排除する",
			key:       "/meta/id",
			recs:      []string{`{"meta":{"id":7}}`, `{"meta":{"id":7}}`, `{"meta":{"id":8}}`},
			wantOut:   []string{`{"meta":{"id":7}}`, ``, `{"meta":{"id":8}}`},
			wantSkips: []bool{false, true, false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{DedupeKey: mustPointer(t, tt.key)}
			p := newPipeline(cfg)
			for i, rec := range tt.recs {
				out, skip, err := p.Apply(jsontext.Value(rec))
				if err != nil {
					t.Fatalf("%d 件目の Apply が失敗しました: %v", i, err)
				}
				if skip != tt.wantSkips[i] {
					t.Fatalf("%d 件目の skip が一致しません: got=%v want=%v", i, skip, tt.wantSkips[i])
				}
				if skip {
					continue
				}
				if string(out) != tt.wantOut[i] {
					t.Errorf("%d 件目の出力が一致しません\n got: %s\nwant: %s", i, out, tt.wantOut[i])
				}
			}
		})
	}
}

// 重複排除の状態はパイプラインごとに独立する(同一実行内のみが対象、要件 6.1)。
func TestPipelineDedupeScopeIsPerPipeline(t *testing.T) {
	cfg := &Config{DedupeKey: mustPointer(t, "/id")}

	p1 := newPipeline(cfg)
	if _, skip, err := p1.Apply(jsontext.Value(`{"id":1}`)); err != nil || skip {
		t.Fatalf("1 本目の 1 件目: skip=%v err=%v", skip, err)
	}
	if _, skip, err := p1.Apply(jsontext.Value(`{"id":1}`)); err != nil || !skip {
		t.Fatalf("1 本目の 2 件目はスキップされるべきです: skip=%v err=%v", skip, err)
	}

	p2 := newPipeline(cfg)
	if _, skip, err := p2.Apply(jsontext.Value(`{"id":1}`)); err != nil || skip {
		t.Fatalf("2 本目の 1 件目は p1 の状態を引き継いではいけません: skip=%v err=%v", skip, err)
	}
}

// --- (5) 組み合わせ(置換 + 追加 + 重複排除)と適用順序 --------------------------

func TestPipelineApplyCombination(t *testing.T) {
	t.Run("置換+追加+重複排除が固定順序で適用される", func(t *testing.T) {
		cfg := &Config{
			HyphenToUnder: true,
			AddFields:     []AddField{{Key: "src", Value: "api"}},
			DedupeKey:     mustPointer(t, "/user_id"), // 置換「後」の名前で解決される
		}
		p := newPipeline(cfg)

		recs := []string{`{"user-id":1,"v":"a"}`, `{"user-id":2,"v":"b"}`, `{"user-id":1,"v":"c"}`}
		wantOut := []string{`{"user_id":1,"v":"a","src":"api"}`, `{"user_id":2,"v":"b","src":"api"}`, ``}
		wantSkip := []bool{false, false, true}

		for i, rec := range recs {
			out, skip, err := p.Apply(jsontext.Value(rec))
			if err != nil {
				t.Fatalf("%d 件目の Apply が失敗しました: %v", i, err)
			}
			if skip != wantSkip[i] {
				t.Fatalf("%d 件目の skip が一致しません: got=%v want=%v", i, skip, wantSkip[i])
			}
			if !skip && string(out) != wantOut[i] {
				t.Errorf("%d 件目の出力が一致しません\n got: %s\nwant: %s", i, out, wantOut[i])
			}
		}
	})

	// 逆向きの対照実験: 置換前の名前を重複排除キーにすると 1 件も一致しない。
	// これが失敗する場合、置換が重複排除より後に適用されている。
	t.Run("置換前の名前を指定した重複排除は一致しない", func(t *testing.T) {
		cfg := &Config{
			HyphenToUnder: true,
			DedupeKey:     mustPointer(t, "/user-id"),
		}
		p := newPipeline(cfg)
		for i, rec := range []string{`{"user-id":1}`, `{"user-id":1}`, `{"user-id":1}`} {
			out, skip, err := p.Apply(jsontext.Value(rec))
			if err != nil {
				t.Fatalf("%d 件目の Apply が失敗しました: %v", i, err)
			}
			if skip {
				t.Fatalf("%d 件目がスキップされました(置換後の名前でのみ一致するはずです)", i)
			}
			if string(out) != `{"user_id":1}` {
				t.Errorf("%d 件目の出力が一致しません: %s", i, out)
			}
		}
	})

	// 置換 → 追加の順序: 置換後の名前と固定値追加のキーが衝突する。
	t.Run("置換後の名前と固定値追加のキーが衝突したら終了コード 1", func(t *testing.T) {
		cfg := &Config{
			HyphenToUnder: true,
			AddFields:     []AddField{{Key: "a_b", Value: "x"}},
		}
		_, _, err := applyOne(t, cfg, `{"a-b":1}`)
		if code := exitCodeOf(t, err); code != ExitUsage {
			t.Fatalf("終了コードが一致しません: got=%d want=%d (err=%v)", code, ExitUsage, err)
		}
	})

	// 追加は置換の「後」なので、追加するキー名の - は置換されない。
	t.Run("固定値追加のキー名は置換されない", func(t *testing.T) {
		cfg := &Config{
			HyphenToUnder: true,
			AddFields:     []AddField{{Key: "x-y", Value: "v"}},
		}
		got, _, err := applyOne(t, cfg, `{"a-b":1}`)
		if err != nil {
			t.Fatalf("Apply が失敗しました: %v", err)
		}
		if want := `{"a_b":1,"x-y":"v"}`; got != want {
			t.Errorf("出力が一致しません\n got: %s\nwant: %s", got, want)
		}
	})

	// 追加 → サイズ判定の順序: 追加した分を含めたバイト数で上限を判定する。
	t.Run("サイズ上限は固定値追加の後のバイト数で判定される", func(t *testing.T) {
		// {"a":1} = 7 バイト、追加後 {"a":1,"b":"x"} = 15 バイト。
		cfg := &Config{AddFields: []AddField{{Key: "b", Value: "x"}}, MaxRecordBytes: 14}
		_, _, err := applyOne(t, cfg, `{"a":1}`)
		if code := exitCodeOf(t, err); code != ExitLocation {
			t.Fatalf("終了コードが一致しません: got=%d want=%d (err=%v)", code, ExitLocation, err)
		}

		cfg.MaxRecordBytes = 15
		got, skip, err := applyOne(t, cfg, `{"a":1}`)
		if err != nil || skip {
			t.Fatalf("上限ちょうどは通るはずです: skip=%v err=%v", skip, err)
		}
		if want := `{"a":1,"b":"x"}`; got != want {
			t.Errorf("出力が一致しません\n got: %s\nwant: %s", got, want)
		}
	})

	// サイズ判定 → 重複排除の順序: スキップしたレコードは重複排除の集合へ登録されない。
	t.Run("スキップした過大レコードは重複排除の集合へ登録されない", func(t *testing.T) {
		cfg := &Config{
			MaxRecordBytes: 12,
			SkipOversize:   true,
			DedupeKey:      mustPointer(t, "/id"),
		}
		p := newPipeline(cfg)
		p.SetWarnWriter(&bytes.Buffer{})

		if _, skip, err := p.Apply(jsontext.Value(`{"id":1,"pad":"xxxx"}`)); err != nil || !skip {
			t.Fatalf("過大レコードはスキップされるべきです: skip=%v err=%v", skip, err)
		}
		out, skip, err := p.Apply(jsontext.Value(`{"id":1}`))
		if err != nil {
			t.Fatalf("Apply が失敗しました: %v", err)
		}
		if skip {
			t.Fatal("スキップされたレコードのキーが登録されています(サイズ判定は重複排除より前です)")
		}
		if string(out) != `{"id":1}` {
			t.Errorf("出力が一致しません: %s", out)
		}
	})
}

// --- (6) 字句保持(本タスク最大のリスク、要件 3.7-3.9 の維持)--------------------

func TestPipelineApplyPreservesLexemes(t *testing.T) {
	// 数値の生字句、\uXXXX エスケープ、サロゲートペア、絵文字、制御文字エスケープ。
	// é / 😀(サロゲートペア)は UTF-8 化してはならず、
	// 逆に生の é / 😀 を \uXXXX 化してもならない(要件 3.8, 3.9)。
	const body = `"one":1.0,"exp":1e10,"big":123456789012345678901234567890,` +
		`"prec":0.12345678901234567890,"neg":-0.0,"zero":0e0,` +
		"\"escaped\":\"\\u00e9\\ud83d\\ude00\"," +
		`"ctrl":"\n\t\"\\\/\b\f\r",` +
		`"utf8":"café😀","html":"<>&"`

	tests := []struct {
		name string
		cfg  *Config
		rec  string
		want string
	}{
		{
			name: "キー名置換の再エンコード経路で値の字句が 1 バイトも変わらない",
			cfg:  &Config{HyphenToUnder: true},
			rec:  `{"a-b":0,` + body + `}`,
			want: `{"a_b":0,` + body + `}`,
		},
		{
			name: "固定値追加の再エンコード経路で値の字句が 1 バイトも変わらない",
			cfg:  &Config{AddFields: []AddField{{Key: "z", Value: "1"}}},
			rec:  `{` + body + `}`,
			want: `{` + body + `,"z":"1"}`,
		},
		{
			name: "ネストした値の字句も保持する",
			cfg:  &Config{HyphenToUnder: true},
			rec:  `{"n-e":{"deep":[1.0,1e10,"é"]}}`,
			want: `{"n_e":{"deep":[1.0,1e10,"é"]}}`,
		},
		{
			name: "整形が無効なら入力をそのまま返す",
			cfg:  &Config{},
			rec:  `{` + body + `}`,
			want: `{` + body + `}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, skip, err := applyOne(t, tt.cfg, tt.rec)
			if err != nil {
				t.Fatalf("Apply が失敗しました: %v", err)
			}
			if skip {
				t.Fatal("skip=true になりました")
			}
			if got != tt.want {
				t.Errorf("字句が保持されていません\n got: %s\nwant: %s", got, tt.want)
			}
		})
	}
}

// --- (7) 素通し経路と不変条件 ----------------------------------------------------

// 整形オプションが 1 つも有効でなければ、再エンコードを経ずに入力そのものを返す。
// 同一のバッキング配列を返すことで、バイト同一性が「たまたま」ではなく構造的に保証される。
func TestPipelineApplyPassthroughSharesBuffer(t *testing.T) {
	rec := jsontext.Value(`{"a":1,"b":[1.0,"x"]}`)
	p := newPipeline(&Config{})
	out, skip, err := p.Apply(rec)
	if err != nil || skip {
		t.Fatalf("Apply が失敗しました: skip=%v err=%v", skip, err)
	}
	if len(out) != len(rec) || &out[0] != &rec[0] {
		t.Fatalf("素通し経路で再エンコードが行われています: out=%s", out)
	}

	// 重複排除・サイズ上限だけを有効にしても素通し経路のままであること。
	p2 := newPipeline(&Config{DedupeKey: mustPointer(t, "/a"), MaxRecordBytes: 1024})
	out2, skip2, err2 := p2.Apply(rec)
	if err2 != nil || skip2 {
		t.Fatalf("Apply が失敗しました: skip=%v err=%v", skip2, err2)
	}
	if len(out2) != len(rec) || &out2[0] != &rec[0] {
		t.Fatalf("重複排除・サイズ上限のみで再エンコードが行われています: out=%s", out2)
	}
}

func TestPipelineApplyDoesNotMutateInput(t *testing.T) {
	cases := []struct {
		name string
		cfg  *Config
		rec  string
	}{
		{name: "キー名置換", cfg: &Config{HyphenToUnder: true}, rec: `{"a-b":{"c-d":[{"e-f":1}]}}`},
		{name: "固定値追加", cfg: &Config{AddFields: []AddField{{Key: "z", Value: "9"}}}, rec: `{"a":1}`},
		{
			name: "組み合わせ",
			cfg: &Config{
				HyphenToUnder: true,
				AddFields:     []AddField{{Key: "z", Value: "9"}},
				DedupeKey:     mustPointer(t, "/a_b"),
			},
			rec: `{"a-b":1}`,
		},
		{name: "素通し", cfg: &Config{}, rec: `{"a":1}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := jsontext.Value(tc.rec)
			before := bytes.Clone(rec)
			p := newPipeline(tc.cfg)
			if _, _, err := p.Apply(rec); err != nil {
				t.Fatalf("Apply が失敗しました: %v", err)
			}
			if !bytes.Equal(rec, before) {
				t.Errorf("入力が書き換えられました\n after: %s\nbefore: %s", rec, before)
			}
		})
	}
}

// --- (8) extract(位置指定の値抽出、要件 5.3, 5.5)--------------------------------

func TestExtract(t *testing.T) {
	const rec = `{"a":{"b":[10,20,30],"s":"x"},"n":1.0,"o":{"p":1},"":"empty-key","a/b":"slash","a~b":"tilde"}`

	tests := []struct {
		name         string
		rec          string
		ptr          string
		want         string
		wantNotFound bool
	}{
		{name: "ルートはレコード全体", rec: `{"a":1}`, ptr: "", want: `{"a":1}`},
		{name: "トップレベルのキー", rec: rec, ptr: "/n", want: `1.0`},
		{name: "数値は生字句のまま返す", rec: `{"n":1e10}`, ptr: "/n", want: `1e10`},
		{name: "大きな整数も生字句のまま", rec: `{"n":123456789012345678901234567890}`, ptr: "/n", want: `123456789012345678901234567890`},
		{name: "ネストした位置", rec: rec, ptr: "/a/s", want: `"x"`},
		{name: "配列添字", rec: rec, ptr: "/a/b/1", want: `20`},
		{name: "配列添字 0", rec: rec, ptr: "/a/b/0", want: `10`},
		{name: "非スカラー(オブジェクト)もそのまま返す", rec: rec, ptr: "/o", want: `{"p":1}`},
		{name: "非スカラー(配列)もそのまま返す", rec: rec, ptr: "/a/b", want: `[10,20,30]`},
		{name: "空文字列キーはルートと区別される", rec: rec, ptr: "/", want: `"empty-key"`},
		{name: "~1 でエスケープされた / を含むキー", rec: rec, ptr: "/a~1b", want: `"slash"`},
		{name: "~0 でエスケープされた ~ を含むキー", rec: rec, ptr: "/a~0b", want: `"tilde"`},
		{name: `\uXXXX で書かれたキーとも一致する`, rec: "{\"a\\u002db\":1}", ptr: "/a-b", want: `1`},
		{name: "エスケープを含む値は生字句のまま返す", rec: "{\"k\":\"\\u00e9\"}", ptr: "/k", want: `"\u00e9"`},

		{name: "存在しないキー", rec: rec, ptr: "/zzz", wantNotFound: true},
		{name: "存在しないネストキー", rec: rec, ptr: "/a/zzz", wantNotFound: true},
		{name: "配列の範囲外の添字", rec: rec, ptr: "/a/b/9", wantNotFound: true},
		{name: "先頭ゼロの添字は添字として扱わない", rec: rec, ptr: "/a/b/01", wantNotFound: true},
		{name: "配列末尾を指す - は添字として扱わない", rec: rec, ptr: "/a/b/-", wantNotFound: true},
		{name: "配列に対する非添字トークン", rec: rec, ptr: "/a/b/x", wantNotFound: true},
		{name: "スカラーの内側へは降りられない", rec: rec, ptr: "/n/x", wantNotFound: true},
		{name: "オブジェクトに対する添字トークンはキーとして扱われ一致しない", rec: `{"a":1}`, ptr: "/0", wantNotFound: true},
		{name: "空オブジェクト", rec: `{}`, ptr: "/a", wantNotFound: true},
		{name: "空配列", rec: `[]`, ptr: "/0", wantNotFound: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ptr := mustPointer(t, tt.ptr)
			got, err := extract(jsontext.Value(tt.rec), *ptr)
			if tt.wantNotFound {
				if !errors.Is(err, errNotFound) {
					t.Fatalf("errNotFound を期待しましたが got=%v (値=%s)", err, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("extract が失敗しました: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("抽出結果が一致しません\n got: %s\nwant: %s", got, tt.want)
			}
		})
	}
}

// extract は入力レコードを書き換えない。
func TestExtractDoesNotMutateInput(t *testing.T) {
	rec := jsontext.Value(`{"a":{"b":[1,2,3]}}`)
	before := bytes.Clone(rec)
	if _, err := extract(rec, Pointer{"a", "b", "2"}); err != nil {
		t.Fatalf("extract が失敗しました: %v", err)
	}
	if !bytes.Equal(rec, before) {
		t.Errorf("入力が書き換えられました\n after: %s\nbefore: %s", rec, before)
	}
}

// --- (9) カーソル値の整形と検証(要件 5.3, 5.5, 5.6 / FR-29, FR-30, FR-31)-------

func TestExtractCursorValue(t *testing.T) {
	tests := []struct {
		name     string
		rec      string
		ptr      string
		want     string
		wantCode int
	}{
		{name: "文字列は引用符を外して内容のみ", rec: `{"id":"abc"}`, ptr: "/id", want: "abc"},
		{name: "文字列のエスケープは解釈する", rec: "{\"id\":\"a\\u0041b\"}", ptr: "/id", want: "aAb"},
		{name: "数値は生字句のまま", rec: `{"id":1.0}`, ptr: "/id", want: "1.0"},
		{name: "大きな整数も生字句のまま", rec: `{"id":123456789012345678901234567890}`, ptr: "/id", want: "123456789012345678901234567890"},
		{name: "指数表記も生字句のまま", rec: `{"id":1e10}`, ptr: "/id", want: "1e10"},
		{name: "真偽値", rec: `{"id":true}`, ptr: "/id", want: "true"},
		{name: "null", rec: `{"id":null}`, ptr: "/id", want: "null"},
		{name: "空文字列", rec: `{"id":""}`, ptr: "/id", want: ""},
		{name: "ネストした位置", rec: `{"m":{"id":"z9"}}`, ptr: "/m/id", want: "z9"},
		{name: "配列添字", rec: `{"a":[{"id":"k1"},{"id":"k2"}]}`, ptr: "/a/1/id", want: "k2"},

		{name: "位置が存在しなければ終了コード 5", rec: `{"a":1}`, ptr: "/id", wantCode: ExitLocation},
		{name: "オブジェクトは終了コード 5", rec: `{"id":{"a":1}}`, ptr: "/id", wantCode: ExitLocation},
		{name: "配列は終了コード 5", rec: `{"id":[1]}`, ptr: "/id", wantCode: ExitLocation},
		{name: "非 ASCII の文字列は終了コード 5", rec: `{"id":"あ"}`, ptr: "/id", wantCode: ExitLocation},
		{name: "エスケープされた非 ASCII も終了コード 5", rec: "{\"id\":\"a\\u00e9b\"}", ptr: "/id", wantCode: ExitLocation},
		{name: "空白を含む文字列は終了コード 5", rec: `{"id":"a b"}`, ptr: "/id", wantCode: ExitLocation},
		{name: "制御文字を含む文字列は終了コード 5", rec: "{\"id\":\"a\\u0009b\"}", ptr: "/id", wantCode: ExitLocation},
		{name: "改行を含む文字列は終了コード 5", rec: `{"id":"a\nb"}`, ptr: "/id", wantCode: ExitLocation},
		{name: "DEL(0x7f)を含む文字列は終了コード 5", rec: "{\"id\":\"a\\u007fb\"}", ptr: "/id", wantCode: ExitLocation},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ptr := mustPointer(t, tt.ptr)
			got, err := extractCursorValue(jsontext.Value(tt.rec), *ptr)
			if tt.wantCode != 0 {
				if code := exitCodeOf(t, err); code != tt.wantCode {
					t.Fatalf("終了コードが一致しません: got=%d want=%d (err=%v)", code, tt.wantCode, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("extractCursorValue が失敗しました: %v", err)
			}
			if got != tt.want {
				t.Errorf("カーソル値が一致しません: got=%q want=%q", got, tt.want)
			}
		})
	}
}

// --- (10) 9.4 / NFR-05 のヘルプ文言への引き渡し ----------------------------------

// dedupeMemoryNote は --help へ埋め込むための文言(タスク 4.1 が version.go / cli.go で使う)。
// 要件 9.4 は「上限を超えうる旨を文書化する」ことを求めており、その文言をここで確定する。
func TestDedupeMemoryNote(t *testing.T) {
	if dedupeMemoryNote == "" {
		t.Fatal("dedupeMemoryNote が空です")
	}
	// 要件 9.4 / NFR-05 の主張そのものを拘束する。語の存在だけを見ていると、
	// 注記を逆の意味(「上限の範囲で安全に動作する」等)へ書き換えても検出できない。
	for _, want := range []string{"--dedupe-key", "256", "数百万", "超え"} {
		if !strings.Contains(dedupeMemoryNote, want) {
			t.Errorf("dedupeMemoryNote に %q が含まれていません: %s", want, dedupeMemoryNote)
		}
	}
	if strings.ContainsAny(dedupeMemoryNote, "\n\r") {
		t.Errorf("dedupeMemoryNote は 1 行の文言である必要があります: %q", dedupeMemoryNote)
	}
}
