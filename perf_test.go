package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// perf_test.go は 300 MB 級の入力に対する処理時間とメモリのピークを計測する
// (design.md「Testing Strategy / Performance」、TEST-02 / TEST-03)。
//
// フィクスチャはリポジトリに含めずテスト内で生成し、いずれのテストも
// `go test -short` でスキップできる(TEST-02)。通常の単体テストと比べて
// 実行時間とディスクを大きく使うため、既定の `go test ./...` では走るが
// CI の短縮実行では外せるようにしている。
//
// 計測値は合否ではなく報告が目的のものと、要件が上限を定めていて
// アサーションになるものの 2 種類がある。
//   - 処理時間(要件 9.3 / NFR-03): 目標時間は「実機計測で確定」と定められており、
//     本テストはその初回確定に使える実測値を t.Log で報告する。閾値は設けない
//     (開発機の速度で合否が決まるテストは、要件が求めていない上に不安定になる)
//   - メモリのピーク(要件 9.2 / NFR-02): 上限の目安 256 MB が要件に書かれているため、
//     アサーションにする

// memoryLimitBytes は要件 9.2 / NFR-02 が定めるメモリ使用量の上限の目安。
const memoryLimitBytes = 256 << 20

// perfInputBytes は要件 9.3 / NFR-03 が定める入力サイズ(300 MB)。
const perfInputBytes = 300 << 20

// heapSampleInterval はメモリのピーク採取の間隔。
//
// runtime.ReadMemStats は stop-the-world を伴うため、間隔を詰めすぎると
// 同じ実行で採る処理時間の計測値を汚す。20 ms なら 10 秒の実行でも 500 回で
// オーバーヘッドは無視できる一方、「入力サイズに比例してメモリが増える」
// 実装であれば秒単位で単調増加するので取り逃さない。
const heapSampleInterval = 20 * time.Millisecond

// skipIfShort は -short 指定時にテストをスキップする(TEST-02)。
func skipIfShort(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("大きな入力を生成するため -short ではスキップする(TEST-02)")
	}
}

// generateArray は合計が target バイト以上になるまでレコードを並べた JSON 配列を
// path へ書き出し、実際のバイト数とレコード数を返す。
//
// テストプロセス自身が入力全体を抱えないよう、バッファ経由で逐次書き出す。
// レコードの形は API レスポンスに寄せてあり(数値・文字列の金額・日時・
// ハイフン入りキー・ネストしたオブジェクト)、payload の長さで 1 レコードの
// 大きさを調節する。
func generateArray(t *testing.T, path string, target int64, payload string) (size int64, records int) {
	t.Helper()

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("入力ファイル %q を作成できません: %v", path, err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Fatalf("入力ファイル %q を閉じられません: %v", path, err)
		}
	}()

	w := bufio.NewWriterSize(f, 1<<20)
	write := func(b []byte) {
		n, err := w.Write(b)
		if err != nil {
			t.Fatalf("入力ファイル %q へ書き出せません: %v", path, err)
		}
		size += int64(n)
	}

	write([]byte("["))
	var buf []byte
	for size < target {
		buf = buf[:0]
		if records > 0 {
			buf = append(buf, ',')
		}
		id := records + 1
		buf = fmt.Appendf(buf,
			`{"id":%d,"name":"item-%d","amount":"1234.50","updated-at":"2026-08-25T00:00:00Z","nested":{"tags":["a","b"]},"payload":%q}`,
			id, id, payload)
		write(buf)
		records++
	}
	write([]byte("]"))

	if err := w.Flush(); err != nil {
		t.Fatalf("入力ファイル %q を書き切れません: %v", path, err)
	}
	return size, records
}

// heapMeasurement は measurePeakHeap の計測結果。
//
// 絶対値(Peak)と増分(Delta)を使い分ける。要件 9.2 / NFR-02 の上限 256 MB は
// プロセス全体のメモリに対する上限なので絶対値で見る。一方「1 レコードあたり
// いくら使うか」を測るときは増分で見る。テストプロセスは同じプロセス内で
// 他のテストの後に走るため、絶対値には直前のテストが残したヒープが混ざり、
// レコードが小さいほど倍率が過大に出るためである(実測: 8 MiB のレコードが
// 単独実行では 5.0x、全テスト実行では 8.0x に見えた)。
type heapMeasurement struct {
	// Baseline は計測開始時(GC 直後)の HeapAlloc。
	Baseline uint64
	// Peak は計測中の HeapAlloc の最大値。Baseline 以上であることが保証される。
	Peak uint64
}

// Delta は計測対象が上積みしたヒープの量。
func (m heapMeasurement) Delta() uint64 { return m.Peak - m.Baseline }

// measurePeakHeap は fn の実行中の runtime.MemStats.HeapAlloc のピークを測る(TEST-03)。
//
// 計測前に GC を 2 回走らせ、直前のテストが残したごみとファイナライザ待ちを片付けてから
// ベースラインを取る。fn の終了直後にもう一度読むことで、最後の採取から終了までの間に
// 伸びた分も拾う。
func measurePeakHeap(t *testing.T, fn func()) heapMeasurement {
	t.Helper()

	runtime.GC()
	runtime.GC()

	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	baseline := base.HeapAlloc

	var peak atomic.Uint64
	peak.Store(baseline)
	sample := func() {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		peak.Store(max(peak.Load(), ms.HeapAlloc))
	}

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		ticker := time.NewTicker(heapSampleInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				sample()
			}
		}
	})

	fn()

	close(done)
	wg.Wait()
	sample()
	return heapMeasurement{Baseline: baseline, Peak: peak.Load()}
}

// resultRecords は結果行から records の値を取り出す。
func resultRecords(t *testing.T, stdout string) int {
	t.Helper()
	for f := range strings.FieldsSeq(stdout) {
		if key, value, ok := strings.Cut(f, "="); ok && key == "records" {
			n, err := strconv.Atoi(value)
			if err != nil {
				t.Fatalf("結果行の records が数値ではありません: %q", stdout)
			}
			return n
		}
	}
	t.Fatalf("結果行に records がありません: %q", stdout)
	return 0
}

func mib(b uint64) string { return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20)) }

// TestPerf300MB は 300 MB の入力 1 ファイルを処理し、処理時間(要件 9.3)を報告し、
// メモリのピークが上限内に収まること(要件 9.1 / 9.2)を確かめる
// (design.md の要件対応表: 9.3 → perf_test / TestPerf300MB)。
func TestPerf300MB(t *testing.T) {
	skipIfShort(t)

	dir := t.TempDir()
	in := filepath.Join(dir, "large.json")
	out := filepath.Join(dir, "large.ndjson")

	genStart := time.Now()
	size, records := generateArray(t, in, perfInputBytes, strings.Repeat("x", 128))
	t.Logf("入力を生成: %s / %d レコード(%v)", mib(uint64(size)), records, time.Since(genStart).Round(time.Millisecond))

	var (
		code    int
		stdout  string
		stderr  string
		elapsed time.Duration
	)
	mem := measurePeakHeap(t, func() {
		start := time.Now()
		code, stdout, stderr = runCLI(t, "--in", in, "--out", out)
		elapsed = time.Since(start)
	})

	if code != ExitOK {
		t.Fatalf("終了コード = %d, want %d (stderr=%q)", code, ExitOK, stderr)
	}
	if got := resultRecords(t, stdout); got != records {
		t.Errorf("records = %d, want %d", got, records)
	}

	// 処理時間は報告のみ(要件 9.3 の目標時間は実機計測で確定するため、
	// 開発機の速度を閾値にしない)。DataSpider のタイムアウトは NFR-03 により
	// 目標時間の 2 倍以上を設定するので、その目安も併せて出す。
	throughput := float64(size) / (1 << 20) / elapsed.Seconds()
	t.Logf("処理時間: %v(%.1f MiB/s)", elapsed.Round(time.Millisecond), throughput)
	t.Logf("要件 9.3 / NFR-03 の目安: この実測値をもとに目標時間を確定し、DataSpider のタイムアウトはその 2 倍以上(この実測なら %v 以上)を設定する",
		(2 * elapsed).Round(time.Second))

	// メモリのピーク(要件 9.2 / NFR-02)。
	t.Logf("ヒープのピーク: %s(入力 %s の %.1f%%)", mib(mem.Peak), mib(uint64(size)), 100*float64(mem.Peak)/float64(size))
	if mem.Peak > memoryLimitBytes {
		t.Errorf("ヒープのピーク %s が上限 %s を超えた(要件 9.2 / NFR-02)", mib(mem.Peak), mib(memoryLimitBytes))
	}
	// ストリーミング処理の証拠(要件 9.1 / NFR-01)。入力全体をメモリに展開する
	// 実装ならピークは入力サイズ以上になる。上限 256 MB は 300 MB 入力に対して
	// わずかに小さいだけなので、それだけでは「展開していない」ことの証明として弱い。
	if half := uint64(size) / 2; mem.Peak > half {
		t.Errorf("ヒープのピーク %s が入力の半分 %s を超えた。入力をストリーミングで処理していない疑いがある(要件 9.1 / NFR-01)",
			mib(mem.Peak), mib(half))
	}
}

// TestPerfMemoryIsIndependentOfInputSize は、入力サイズを 8 倍にしても
// メモリのピークがほとんど変わらないことを確かめる(要件 9.2 / NFR-02 の
// 「入力ファイルサイズに依存しない」)。
//
// 絶対値の上限(TestPerf300MB)だけでは、上限に収まりつつサイズに比例して
// 増える実装を捕まえられない。ここでは 2 点の差を見る。
func TestPerfMemoryIsIndependentOfInputSize(t *testing.T) {
	skipIfShort(t)

	const (
		small = 16 << 20
		large = 128 << 20
		// 許容差。入力サイズの差は 112 MiB あるため、入力に比例して増える実装なら
		// 大きく超える。バッファやレコード 1 件分の揺らぎは数 MiB に収まる。
		slack = 32 << 20
	)

	measure := func(target int64) uint64 {
		dir := t.TempDir()
		in := filepath.Join(dir, "in.json")
		out := filepath.Join(dir, "out.ndjson")
		generateArray(t, in, target, strings.Repeat("y", 128))
		var code int
		var stderr string
		mem := measurePeakHeap(t, func() {
			code, _, stderr = runCLI(t, "--in", in, "--out", out)
		})
		if code != ExitOK {
			t.Fatalf("終了コード = %d, want %d (stderr=%q)", code, ExitOK, stderr)
		}
		return mem.Peak
	}

	peakSmall := measure(small)
	peakLarge := measure(large)
	t.Logf("ヒープのピーク: 入力 %s → %s / 入力 %s → %s",
		mib(small), mib(peakSmall), mib(large), mib(peakLarge))

	if peakLarge > peakSmall+slack {
		t.Errorf("入力を %s から %s へ増やすとヒープのピークが %s から %s へ増えた(許容差 %s)。"+
			"メモリ使用量が入力サイズに依存している疑いがある(要件 9.2 / NFR-02)",
			mib(small), mib(large), mib(peakSmall), mib(peakLarge), mib(slack))
	}
}

// hugeRecordInput は「小さいレコード → 巨大レコード → 小さいレコード」の順に
// 並べた入力を作り、パスと巨大レコードのおおよそのバイト数を返す。
func hugeRecordInput(t *testing.T, dir string, hugeBytes int) string {
	t.Helper()
	path := filepath.Join(dir, "huge.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("入力ファイル %q を作成できません: %v", path, err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Fatalf("入力ファイル %q を閉じられません: %v", path, err)
		}
	}()

	w := bufio.NewWriterSize(f, 1<<20)
	if _, err := fmt.Fprintf(w, `[{"id":1},{"id":2,"payload":%q},{"id":3}]`, strings.Repeat("z", hugeBytes)); err != nil {
		t.Fatalf("入力ファイル %q へ書き出せません: %v", path, err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("入力ファイル %q を書き切れません: %v", path, err)
	}
	return path
}

// TestPerfHugeRecordExceedsLimit は、上限を超える巨大レコードが
// --skip-oversize なしでは終了コード 5 になること(要件 6.5 / NFR-04)と、
// その処理中もメモリが上限内に収まることを確かめる。
func TestPerfHugeRecordExceedsLimit(t *testing.T) {
	skipIfShort(t)

	dir := t.TempDir()
	in := hugeRecordInput(t, dir, 32<<20)
	out := filepath.Join(dir, "out.ndjson")

	var (
		code   int
		stdout string
		stderr string
	)
	mem := measurePeakHeap(t, func() {
		code, stdout, stderr = runCLI(t, "--in", in, "--out", out, "--max-record-bytes", "1048576")
	})

	if code != ExitLocation {
		t.Fatalf("終了コード = %d, want %d (stderr=%q)", code, ExitLocation, stderr)
	}
	if stdout != "" {
		t.Errorf("異常終了では標準出力へ書かない(要件 8.1)。stdout = %q", stdout)
	}
	if stderr == "" {
		t.Error("異常終了では標準エラー出力へ理由を書く(要件 8.2)")
	}
	// 新規モードの異常終了では出力パスにファイルを残さない(要件 4.2 / 4.9)。
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Errorf("異常終了後に出力ファイルが残っている: err = %v", err)
	}

	t.Logf("ヒープのピーク: %s(巨大レコード 32.0 MiB)", mib(mem.Peak))
	if mem.Peak > memoryLimitBytes {
		t.Errorf("ヒープのピーク %s が上限 %s を超えた(要件 9.2 / NFR-02)", mib(mem.Peak), mib(memoryLimitBytes))
	}
}

// TestPerfHugeRecordSkippedWithWarning は、--skip-oversize 指定時に巨大レコードが
// 標準エラー出力への警告とともに読み飛ばされ、処理が継続すること(要件 6.6 / NFR-04)を
// 確かめる。
func TestPerfHugeRecordSkippedWithWarning(t *testing.T) {
	skipIfShort(t)

	dir := t.TempDir()
	in := hugeRecordInput(t, dir, 32<<20)
	out := filepath.Join(dir, "out.ndjson")

	var (
		code   int
		stdout string
		stderr string
	)
	mem := measurePeakHeap(t, func() {
		code, stdout, stderr = runCLI(t, "--in", in, "--out", out,
			"--max-record-bytes", "1048576", "--skip-oversize")
	})

	if code != ExitOK {
		t.Fatalf("終了コード = %d, want %d (stderr=%q)", code, ExitOK, stderr)
	}
	if got := resultRecords(t, stdout); got != 2 {
		t.Errorf("records = %d, want 2(巨大レコード 1 件がスキップされる)", got)
	}
	if stderr == "" {
		t.Error("--skip-oversize では標準エラー出力へ警告を出す(要件 6.6 / NFR-04)")
	}

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("出力ファイルを読めません: %v", err)
	}
	if want := "{\"id\":1}\n{\"id\":3}\n"; string(got) != want {
		t.Errorf("出力ファイル = %q, want %q(巨大レコードは含まれない)", got, want)
	}

	t.Logf("ヒープのピーク: %s(巨大レコード 32.0 MiB)", mib(mem.Peak))
	if mem.Peak > memoryLimitBytes {
		t.Errorf("ヒープのピーク %s が上限 %s を超えた(要件 9.2 / NFR-02)", mib(mem.Peak), mib(memoryLimitBytes))
	}
}

// recordHeapMultiplierLimit は 1 レコードのバイト数に対するヒープの増分の倍率の
// 許容上限。実測は 5.0 倍で、全テスト実行を 5 回繰り返しても変わらない
// (TestPerfRecordHeapMultiplier が報告する)。
//
// この値は「現状の実装がこうである」ことを固定する回帰検知用であり、要件が定めた
// ものではない。倍率が上がると要件 9.2 / NFR-02 の 256 MB を破るレコードサイズが
// 小さくなるため、悪化を検出できるようにしてある。
const recordHeapMultiplierLimit = 8.0

// TestPerfRecordHeapMultiplier は、ヒープのピークが「入力ファイルサイズ」ではなく
// 「最大レコードのバイト数」に比例することを確かめ(要件 9.2 / NFR-02 の
// 「入力ファイルサイズに依存せず、最大レコード 1 件分とバッファの合計に収まる」)、
// その比例係数を報告する。
//
// # 報告する事実(タスク 6.1 の計測で判明)
//
// ヒープの増分(ベースラインからの上積み)はレコードのバイト数の約 5.0 倍で、
// 8 / 16 / 32 / 64 MiB のいずれでも倍率は変わらない。したがって要件 9.2 の上限
// 256 MB を破るのは 1 レコードがおおよそ 51 MiB(= 256 / 5)を超えたときである。
//
// 倍率は増分で測る。絶対値のピークで測ると同一プロセス内の他テストが残したヒープが
// 混ざり、レコードが小さいほど倍率が過大に出る(8 MiB のレコードが単独実行では
// 5.0x、全テスト実行では 8.0x に見えた)。heapMeasurement の説明も参照。
//
// これは要件 9.2 と NFR-04 の間の緊張として記録に値する。NFR-04 は受け側の行長制限の
// 例として「BigQuery の JSON ロードは 1 行 100 MB」を挙げており、100 MB のレコードは
// 仕様が想定の範囲に置いている。その大きさのレコードでは本実装のヒープは約 500 MB に
// なり、上限の 2 倍近くになる。
//
// なお --max-record-bytes ではこれを防げない。サイズ判定は整形後のバイト数に対して
// 行われ(タスク 2.2 が確定した契約)、その時点でレコードは既にメモリ上にあるため、
// 上限を指定しても超過レコードの読み込みに伴うヒープは発生する(実測で確認済み:
// TestPerfHugeRecordExceedsLimit と TestPerfHugeRecordSkippedWithWarning の
// ピークは、上限指定のない TestPerfRecordHeapMultiplier の同サイズと一致する)。
func TestPerfRecordHeapMultiplier(t *testing.T) {
	skipIfShort(t)

	for _, sizeMiB := range []int{8, 32} {
		recordBytes := sizeMiB << 20
		dir := t.TempDir()
		in := hugeRecordInput(t, dir, recordBytes)
		out := filepath.Join(dir, "out.ndjson")

		var (
			code   int
			stderr string
		)
		mem := measurePeakHeap(t, func() {
			code, _, stderr = runCLI(t, "--in", in, "--out", out)
		})
		if code != ExitOK {
			t.Fatalf("レコード %d MiB: 終了コード = %d, want %d (stderr=%q)", sizeMiB, code, ExitOK, stderr)
		}

		multiplier := float64(mem.Delta()) / float64(recordBytes)
		breach := float64(memoryLimitBytes) / multiplier / (1 << 20)
		t.Logf("レコード %d MiB → ヒープの増分 %s(ベースライン %s / ピーク %s、倍率 %.1fx、上限 %s を破るのは約 %.0f MiB のレコードから)",
			sizeMiB, mib(mem.Delta()), mib(mem.Baseline), mib(mem.Peak), multiplier, mib(memoryLimitBytes), breach)

		if multiplier > recordHeapMultiplierLimit {
			t.Errorf("レコード %d MiB に対するヒープのピークの倍率が %.1fx で許容上限 %.1fx を超えた。"+
				"要件 9.2 / NFR-02 の 256 MB を破るレコードサイズが小さくなっている",
				sizeMiB, multiplier, recordHeapMultiplierLimit)
		}
	}
}

// generateUniqueIDArray は id が全件異なるレコードを count 件並べた JSON 配列を
// path へ書き出す。--dedupe-key のキー集合メモリの実測(PERF-05)に使うため、
// 重複排除で 1 件も落ちない(= キー集合が最大まで育つ)入力を作る。
func generateUniqueIDArray(t *testing.T, path string, count int) {
	t.Helper()

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("入力ファイル %q を作成できません: %v", path, err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Fatalf("入力ファイル %q を閉じられません: %v", path, err)
		}
	}()

	w := bufio.NewWriterSize(f, 1<<20)
	var buf []byte
	for i := range count {
		buf = buf[:0]
		if i > 0 {
			buf = append(buf, ',')
		} else {
			buf = append(buf, '[')
		}
		buf = fmt.Appendf(buf, `{"id":%d,"v":"x"}`, i+1)
		if _, err := w.Write(buf); err != nil {
			t.Fatalf("入力ファイル %q へ書き出せません: %v", path, err)
		}
	}
	if _, err := w.Write([]byte("]")); err != nil {
		t.Fatalf("入力ファイル %q へ書き出せません: %v", path, err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("入力ファイル %q を書き切れません: %v", path, err)
	}
}

// TestPerfDedupeKeySetMemory は --dedupe-key のキー集合が消費するメモリを
// キー 100 万件と 500 万件で実測する(非機能テスト計画書 PERF-05、要件 9.4 / NFR-05)。
//
// NFR-05 が求めるのは上限の遵守ではなく文書化である。したがって実測値の報告が主目的で、
// 合否条件は「キー件数に対するメモリの増加が線形の想定から大きく外れないこと」に留める。
// 実測から「キー 1 件あたりのバイト数」と「何件で上限 256 MB に達するか」の目安を
// 算出してログに出し、その値を運用文書(ヘルプの dedupeMemoryNote、README)の根拠とする。
//
// キー集合の増分だけを取り出すため、同じ入力を --dedupe-key なしで処理したときの
// 増分をベースラインとして差し引く。
func TestPerfDedupeKeySetMemory(t *testing.T) {
	skipIfShort(t)

	measure := func(count int, dedupe bool) uint64 {
		dir := t.TempDir()
		in := filepath.Join(dir, "in.json")
		out := filepath.Join(dir, "out.ndjson")
		generateUniqueIDArray(t, in, count)

		args := []string{"--in", in, "--out", out}
		if dedupe {
			args = append(args, "--dedupe-key", "/id")
		}
		var (
			code   int
			stdout string
			stderr string
		)
		mem := measurePeakHeap(t, func() {
			code, stdout, stderr = runCLI(t, args...)
		})
		if code != ExitOK {
			t.Fatalf("キー %d 件(dedupe=%v): 終了コード = %d, want %d (stderr=%q)",
				count, dedupe, code, ExitOK, stderr)
		}
		if got := resultRecords(t, stdout); got != count {
			t.Fatalf("キー %d 件(dedupe=%v): records = %d, want %d(全件一意のはず)",
				count, dedupe, got, count)
		}
		return mem.Delta()
	}

	type sample struct {
		count    int
		keySet   uint64  // dedupe あり増分 − dedupe なし増分
		perKey   float64 // キー 1 件あたりのバイト数
		breachAt float64 // 上限 256 MB に達するキー件数の目安
	}
	var samples []sample
	for _, count := range []int{1_000_000, 5_000_000} {
		base := measure(count, false)
		with := measure(count, true)
		// GC のタイミング差で with < base になった場合は 0 として扱う(負の集合はない)。
		var keySet uint64
		if with > base {
			keySet = with - base
		}
		perKey := float64(keySet) / float64(count)
		var breachAt float64
		if perKey > 0 {
			breachAt = float64(memoryLimitBytes) / perKey
		}
		samples = append(samples, sample{count, keySet, perKey, breachAt})
		t.Logf("キー %d 件: キー集合の増分 %s(ベース増分 %s / dedupe あり増分 %s、1 件あたり %.0f バイト、上限 %s に達するのは約 %.0f 万件)",
			count, mib(keySet), mib(base), mib(with), perKey, mib(memoryLimitBytes), breachAt/10_000)
	}

	// 線形性の緩い検査: 件数 5 倍でキー集合が 2〜15 倍の範囲に収まること。
	// map の実装(バケット倍化)により正確な 5 倍にはならないが、桁が合っていれば
	// 「レコード数に比例して増える」という文書化(dedupeMemoryNote)の根拠になる。
	small, large := samples[0], samples[1]
	if small.keySet > 0 {
		ratio := float64(large.keySet) / float64(small.keySet)
		if ratio < 2 || ratio > 15 {
			t.Errorf("キー件数 5 倍に対しキー集合が %.1f 倍。線形の想定(概ね 5 倍)から大きく外れている", ratio)
		}
	}
	// 文書化の整合: ヘルプの注記が実測と矛盾しないことの最低限の確認。
	// dedupeMemoryNote は「レコード数に比例」を謳っているため、存在だけを確かめる
	// (文言の詳細は cli_test.go の TestUsageTextContainsDedupeMemoryNote が固定)。
	if dedupeMemoryNote == "" {
		t.Error("dedupeMemoryNote が空。NFR-05 の文書化義務を満たせない")
	}
}

// TestPerfManyInputFiles は 3 MB × 100 ファイル(合計約 300 MB)の処理を計測する
// (非機能テスト計画書 PERF-06、要件 9.3 / NFR-03、1.2 / FR-02)。
//
// 単一 300 MB との比較で確かめたいのは、メモリのピークがファイル数に比例しないこと。
// 処理時間は報告のみとする(単一ファイルの TestPerf300MB と同じ方針)。
func TestPerfManyInputFiles(t *testing.T) {
	skipIfShort(t)

	const (
		fileCount  = 100
		fileTarget = 3 << 20
	)

	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	args := []string{"--out", out}
	var totalSize int64
	var totalRecords int
	genStart := time.Now()
	for i := range fileCount {
		// 整列(ベース名昇順)後も生成順と一致するよう、幅を固定した連番にする。
		in := filepath.Join(dir, fmt.Sprintf("in-%03d.json", i))
		size, records := generateArray(t, in, fileTarget, strings.Repeat("x", 128))
		totalSize += size
		totalRecords += records
		args = append(args, "--in", in)
	}
	t.Logf("入力を生成: %d ファイル / 合計 %s / %d レコード(%v)",
		fileCount, mib(uint64(totalSize)), totalRecords, time.Since(genStart).Round(time.Millisecond))

	var (
		code    int
		stdout  string
		stderr  string
		elapsed time.Duration
	)
	mem := measurePeakHeap(t, func() {
		start := time.Now()
		code, stdout, stderr = runCLI(t, args...)
		elapsed = time.Since(start)
	})

	if code != ExitOK {
		t.Fatalf("終了コード = %d, want %d (stderr=%q)", code, ExitOK, stderr)
	}
	if got := resultRecords(t, stdout); got != totalRecords {
		t.Errorf("records = %d, want %d", got, totalRecords)
	}
	if want := fmt.Sprintf("files=%d", fileCount); !strings.Contains(stdout, want) {
		t.Errorf("結果行に %q がありません: %q", want, stdout)
	}

	throughput := float64(totalSize) / (1 << 20) / elapsed.Seconds()
	t.Logf("処理時間: %v(%.1f MiB/s)", elapsed.Round(time.Millisecond), throughput)
	t.Logf("ヒープのピーク: %s(合計入力 %s の %.1f%%)",
		mib(mem.Peak), mib(uint64(totalSize)), 100*float64(mem.Peak)/float64(totalSize))

	// メモリはファイル数にも合計サイズにも比例しない(要件 9.2 / NFR-02)。
	// 判定は単一ファイルの TestPerf300MB と同じ絶対上限と、
	// 「1 ファイル分(3 MB)の何倍か」ではなく合計の半分を超えないことで行う。
	if mem.Peak > memoryLimitBytes {
		t.Errorf("ヒープのピーク %s が上限 %s を超えた(要件 9.2 / NFR-02)", mib(mem.Peak), mib(memoryLimitBytes))
	}
	if half := uint64(totalSize) / 2; mem.Peak > half {
		t.Errorf("ヒープのピーク %s が合計入力の半分 %s を超えた。ファイル単位で何かを蓄積している疑いがある(要件 9.1 / NFR-01)",
			mib(mem.Peak), mib(half))
	}
}
