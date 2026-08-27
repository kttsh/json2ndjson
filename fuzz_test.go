package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fuzz_test.go は run 関数のファジングハーネス(非機能テスト計画書 REL-06、
// 要件 10.2 / NFR-08)。
//
// 検査するのは「どんな入力でも定義済みの終了コードで整然と終わる」ことである。
// 不正な JSON が終了コード 3 になるのは正しい挙動であり、検出したいのは
// panic 由来の 9 とハングだけ(ハングは go の fuzz エンジン自身が検出する)。
// あわせて、終了コードによらず成立すべき入出力の規律(要件 8.1〜8.3)を
// 不変条件として検査する。
//
// 実行方法:
//
//	go test -fuzz FuzzRun -fuzztime 30m .   # 変異生成つきの本実行
//	go test -run FuzzRun .                  # シードコーパスのみ(通常の go test に含まれる)
//
// 発見された不具合入力は testdata/fuzz/FuzzRun/ に保存され、以後の go test で
// 回帰テストとして自動実行される。

// fuzzExitCodes は run が返してよい終了コードの集合(要件 7.4 の体系から
// 内部エラーの 9 を除いたもの)。9 は「予期しない内部エラー」であり、
// 入力データだけで到達できたならそれは不具合である。
var fuzzExitCodes = map[int]bool{
	ExitOK:       true,
	ExitUsage:    true,
	ExitInput:    true,
	ExitSyntax:   true,
	ExitOutput:   true,
	ExitLocation: true,
}

func FuzzRun(f *testing.F) {
	// シード: testdata/ の全フィクスチャ(正常系と異常系の両方)。
	// 有効な JSON をシードに与えるとパーサの深部まで届く変異が得られやすい。
	entries, err := os.ReadDir("testdata")
	if err != nil {
		f.Fatalf("testdata を読めません: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join("testdata", e.Name()))
		if err != nil {
			f.Fatalf("シード %q を読めません: %v", e.Name(), err)
		}
		f.Add(data, "", "")
	}
	// 構造の異なる最小シード。ポインタ引数の経路(--path / --cursor-key)にも
	// 変異が届くよう、ポインタ文字列も引数にする。
	f.Add([]byte(`{"data":[{"id":1}]}`), "/data", "/id")
	f.Add([]byte(`[[1,2],[3]]`), "/0", "")
	f.Add([]byte(`"just a string"`), "", "")
	f.Add([]byte("\xef\xbb\xbf[]"), "", "")                          // BOM 付き
	f.Add([]byte("[{\"a\\tb\":\"\\ud83d\\ude00\\u0000\"}]"), "", "") // エスケープ、サロゲートペア、NUL
	f.Add([]byte{0xff, 0xfe, 0x00}, "", "")                          // JSON ですらないバイト列

	f.Fuzz(func(t *testing.T, data []byte, path, cursorKey string) {
		dir := t.TempDir()
		in := filepath.Join(dir, "in.json")
		if err := os.WriteFile(in, data, 0o644); err != nil {
			t.Fatalf("入力ファイルを書けません: %v", err)
		}
		out := filepath.Join(dir, "out.ndjson")

		args := []string{"--in", in, "--out", out}
		if path != "" {
			args = append(args, "--path", path)
		}
		if cursorKey != "" {
			args = append(args, "--cursor-key", cursorKey)
		}

		code, stdout, stderr := runCLI(t, args...)

		// 不変条件 1: 終了コードは定義済みの集合に収まる。9 は panic 経由でしか
		// 出ないはずで、入力データで到達できたなら NFR-08 違反(stderr に
		// panic の内容が入っているため失敗メッセージに含める)。
		if !fuzzExitCodes[code] {
			t.Fatalf("未定義または内部エラーの終了コード %d(args=%q)\nstderr=%q", code, args, stderr)
		}
		// 不変条件 2: 標準出力の規律(要件 8.1)。異常終了で stdout は空、
		// 正常終了で結果行 1 行のみ。
		if code != ExitOK && stdout != "" {
			t.Fatalf("異常終了(code=%d)なのに stdout が空ではない: %q", code, stdout)
		}
		if code == ExitOK {
			if !strings.HasPrefix(stdout, "records=") || strings.Count(stdout, "\n") != 1 || !strings.HasSuffix(stdout, "\n") {
				t.Fatalf("正常終了の stdout が結果行 1 行になっていない: %q", stdout)
			}
			if stderr != "" {
				t.Fatalf("正常終了なのに stderr が空ではない: %q", stderr)
			}
		}
		// 不変条件 3: 新規作成モードの後始末(要件 4.2 / 4.9)。異常終了なら
		// 出力パスにファイルを残さず、どの終了コードでも一時ファイルと
		// ジャーナルの残骸を残さない。
		if code != ExitOK {
			if _, err := os.Lstat(out); !os.IsNotExist(err) {
				t.Fatalf("異常終了(code=%d)後に出力ファイルが残っている: err=%v", code, err)
			}
		}
		leftovers, err := filepath.Glob(filepath.Join(dir, "*.tmp*"))
		if err == nil {
			if j, _ := filepath.Glob(filepath.Join(dir, "*.journal")); len(j) > 0 {
				leftovers = append(leftovers, j...)
			}
		}
		if len(leftovers) > 0 {
			t.Fatalf("一時ファイルまたはジャーナルの残骸がある(code=%d): %v", code, leftovers)
		}
	})
}
