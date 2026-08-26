package main

import (
	"bufio"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// output は出力ファイルの全ライフサイクル(排他・復旧・書き込み・確定・中断)を
// 単独で所有するコンポーネント(design.md「output(出力ファイル管理)」)。
//
// 【このファイルの実装範囲(タスク 3.2)】
// 新規作成モード(--append なし)のみを実装する。追記モードとロールバックは
// タスク 3.3、ジャーナルによる強制終了後の復旧はタスク 3.4 の責務であり、
// それぞれの継ぎ目は openOutput / Finalize / Abort の追記モード分岐に置いてある。
// 追記モードに関わる状態(開始前サイズ、ジャーナルのパス)は、担当タスクが
// OutputManager へ追加する。
//
// 【design.md との差異(実装上の必然)】
// design.md は Finalize の順序を「Flush→Sync→(rename|journal削除)→unlock」と
// 書いているが、新規モードでは rename の前に unlock と Close を行う。Windows の
// os.OpenFile は共有モードに FILE_SHARE_DELETE を含めないため、開いたままの
// ハンドルがあるとリネームが ERROR_SHARING_VIOLATION で失敗する。また Close 自体が
// ロックを解放するため、unlock を Close より後に置くことはできない。
// 「確定(rename)まで出力パスに触れない」という本質は保たれている。

// tempSuffix は一時ファイル名の接尾辞。出力名に付けるだけなので、一時ファイルは
// 必ず出力ファイルと同一ディレクトリに置かれる(要件 11.4 / NFR-13。同一ボリュームで
// あることが os.Rename の成立条件)。名前が決定的であることは、同時実行の検出
// (排他的作成+ロック)と、残骸の stale 判定の前提でもある(design.md「output」)。
const tempSuffix = ".tmp"

// writeBufferSize は出力の書き込みバッファの大きさ(IMPL-15)。
// write システムコールの回数を減らすためのもので、レコード 1 件がこれを超えても
// bufio.Writer が直接書き出すため、メモリ使用量はレコード 1 件分+この値に漸近する。
const writeBufferSize = 64 << 10

// outputMode は出力ファイルの扱い方(design.md の状態遷移図の Init の分岐)。
type outputMode int

const (
	// modeNew は新規作成モード(--append なし)。一時ファイルへ書き、rename で置換する。
	modeNew outputMode = iota
	// modeAppend は追記モード(--append)。タスク 3.3 / 3.4 が実装する。
	modeAppend
)

// outputState は OutputManager の生存状態。Finalize と Abort はどちらか 1 回だけ
// 呼ばれる前提だが(design.md「output」の Preconditions)、main は異常終了経路を
// defer で守るため、確定に成功した直後にも Abort が走りうる。確定済みの出力を
// 後追いの Abort が削除する事故を防ぐため、状態で二重処理を止める。
type outputState int

const (
	// stateOpen は書き込み可能な状態。
	stateOpen outputState = iota
	// stateCommitted は Finalize に成功した状態。出力パスに完全な NDJSON がある。
	stateCommitted
	// stateAborted は中断済みの状態。出力パスは実行前と同じ状態に戻してある。
	stateAborted
)

// OutputManager は出力ファイルの状態機械(design.md「output / Service Interface」)。
// プロセス内では単一ゴルーチンのみが触る前提のため、排他制御は持たない
// (プロセス間の排他は lock コンポーネントが担う)。
type OutputManager struct {
	// cfg は実行設定。出力パス・改行・最終行改行の判断に使う。
	cfg *Config
	// mode は新規作成か追記か。
	mode outputMode
	// state は生存状態。Finalize / Abort の二重実行を止める。
	state outputState
	// newline は行の区切りに使う改行コード(要件 3.3 / FR-12)。
	newline string
	// tempPath は新規作成モードの一時ファイルのパス。
	tempPath string
	// file は書き込み中のファイル。新規作成モードでは一時ファイルを指す。
	file *os.File
	// closed は file を閉じ済みかどうか。二重クローズを避ける。
	closed bool
	// w は file を包む書き込みバッファ。
	w *bufio.Writer
	// wroteAny は 1 件でも書き込んだかどうか。改行の位置(区切りか最終行か)の判断と、
	// レコード 0 件のときに 0 バイトのファイルを残す判断に使う(要件 3.10 / FR-19)。
	wroteAny bool
}

// OutputManager が convert の書き込み境界を満たすことをコンパイル時に保証する。
var _ RecordSink = (*OutputManager)(nil)

// openOutput は排他確保・復旧・追記準備までを行い、以後 WriteRecord を可能にする
// (design.md「output / Service Interface」)。
//
// Preconditions: cfg.Out は絶対パス。
// Postconditions: 返した OutputManager は Finalize か Abort のどちらか 1 回で必ず閉じる。
// 失敗した場合は OutputManager を返さず、出力パスにも一時ファイルにも痕跡を残さない。
func openOutput(cfg *Config) (*OutputManager, error) {
	if cfg.Append {
		// 【継ぎ目】追記モードはタスク 3.3(ロック・開始前サイズ・末尾改行の補完・
		// ロールバック)とタスク 3.4(ジャーナルによる復旧)が実装する。
		// ここは openAppend(cfg) の呼び出しに置き換わる。
		return nil, errAppendNotWired("openOutput")
	}
	return openNew(cfg)
}

// errAppendNotWired は追記モードが未結線であることを表すエラーを返す。
//
// 終了コードに 4(出力書き込みエラー)ではなく 9(予期しない内部エラー)を割り当てる。
// 呼び出し元(DataSpider スクリプト)にとって 4 は「権限やディスク、同時実行など
// 環境側の失敗」であり、リトライや調査の対象になる。未実装の経路がそれを装うと
// 運用者の切り分けを誤らせるため、内部の欠陥として報告する。
func errAppendNotWired(op string) error {
	return &ExitError{
		Code: ExitInternal,
		Err:  fmt.Errorf("%s: 追記モード(--append)は未実装(タスク 3.3 で実装する)", op),
	}
}

// openNew は新規作成モードの準備を行う。
//
// 手順は「既存出力の確認 → 一時ファイルの排他的作成(残骸は stale 判定)→ ロック取得」。
// 既存出力の確認を最初に置くのは、要件 4.7 / FR-26 が「書き込み開始前」の停止を
// 求めているため(design.md「output」の新規モードの記述)。
func openNew(cfg *Config) (*OutputManager, error) {
	if err := checkExistingOutput(cfg); err != nil {
		return nil, err
	}

	tempPath := cfg.Out + tempSuffix
	f, err := createTempExclusive(cfg.Out, tempPath)
	if err != nil {
		return nil, err
	}

	// 一時ファイル名は決定的なので、排他的作成に成功しただけでは同時実行を
	// 検出しきれない(残骸が stale と判定された直後に他プロセスが割り込む余地がある)。
	// ロックを取ることで、以後の実行が「使用中」を見分けられるようにする(要件 4.6 / FR-25)。
	if err := tryLock(f); err != nil {
		_ = f.Close()
		// この一時ファイルは自分が排他的に作成したものなので、消してよい。
		_ = os.Remove(tempPath)
		return nil, &ExitError{
			Code: ExitOutput,
			Err:  fmt.Errorf("出力先 %q の一時ファイル %q をロックできない: %w", cfg.Out, tempPath, err),
		}
	}

	return &OutputManager{
		cfg:      cfg,
		mode:     modeNew,
		state:    stateOpen,
		newline:  newlineOf(cfg),
		tempPath: tempPath,
		file:     f,
		w:        bufio.NewWriterSize(f, writeBufferSize),
	}, nil
}

// newlineOf は行の区切りに使う改行コードを返す(要件 3.3 / FR-12)。
// 値の検証は cli(タスク 4.1)の責務で、ここへ来る時点では "\n" か "\r\n" に
// 限られる。ゼロ値の Config でも既定どおり LF で動くようにだけしておく
// (空文字列のまま使うと全レコードが 1 行へ連結された不正な NDJSON になる)。
func newlineOf(cfg *Config) string {
	if cfg.Newline == "" {
		return "\n"
	}
	return cfg.Newline
}

// checkExistingOutput は、--overwrite なしで出力ファイルが既に存在する場合に
// 終了コード 4 のエラーを返す(要件 4.7 / FR-26)。
//
// os.Stat ではなく os.Lstat を使う。壊れたシンボリックリンクは os.Stat では
// 「存在しない」と見えるが、そのパスは確かに塞がっており、黙って置き換えると
// 利用者の意図しない削除になる。
func checkExistingOutput(cfg *Config) error {
	_, err := os.Lstat(cfg.Out)
	switch {
	case err == nil:
		if cfg.Overwrite {
			return nil
		}
		return &ExitError{
			Code: ExitOutput,
			Err:  fmt.Errorf("出力ファイル %q は既に存在する(置き換えるには --overwrite を指定する)", cfg.Out),
		}
	case errors.Is(err, fs.ErrNotExist):
		return nil
	default:
		return &ExitError{
			Code: ExitOutput,
			Err:  fmt.Errorf("出力ファイル %q の状態を確認できない: %w", cfg.Out, err),
		}
	}
}

// createTempExclusive は一時ファイルを排他的に作成する。
//
// 既存の残骸があった場合は discardStaleTemp が stale 判定を行い、削除できたときだけ
// 作成をもう一度試す。再試行は 1 回に限る(削除の直後に他プロセスが作成した場合に
// 無限に競り合わないようにするため。リトライは呼び出し元の責務、design.md「output」の Risks)。
func createTempExclusive(outPath, tempPath string) (*os.File, error) {
	// O_APPEND は付けない。Windows の syscall.Open は O_APPEND かつ O_TRUNC なしのとき
	// GENERIC_WRITE を落とすため、LockFileEx が要求するアクセス権が残らなくなる
	// (tasks.md「未配線の契約」のタスク 3.3 向けの注記と同じ理由)。
	const flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL

	f, err := os.OpenFile(tempPath, flags, 0o644)
	if err == nil {
		return f, nil
	}
	if !errors.Is(err, fs.ErrExist) {
		return nil, tempCreateError(outPath, tempPath, err)
	}

	if err := discardStaleTemp(outPath, tempPath); err != nil {
		return nil, err
	}

	f, err = os.OpenFile(tempPath, flags, 0o644)
	if err != nil {
		return nil, tempCreateError(outPath, tempPath, err)
	}
	return f, nil
}

// tempCreateError は一時ファイルを作成できないときのエラーを組み立てる。
// 出力先ディレクトリが存在しない、書き込み権限がない、といった事情がここに集まる。
func tempCreateError(outPath, tempPath string, cause error) error {
	return &ExitError{
		Code: ExitOutput,
		Err:  fmt.Errorf("出力先 %q の一時ファイル %q を作成できない: %w", outPath, tempPath, cause),
	}
}

// discardStaleTemp は残存する一時ファイルを stale(前回の強制終了などの残骸)と
// 判定できた場合に削除する(design.md「output」の「既存なら stale 判定」)。
//
// 判定はロックの取得可否で行う。取得できれば誰も使っていない残骸であり削除してよい。
// 取得できなければ同じ出力先を処理中のプロセスがいるということなので、終了コード 4
// で停止する(要件 4.6 / FR-25)。
//
// 【非 Windows での限界】tryLock は no-op で常に成功するため、この関数は残骸を
// 常に stale と判定する。すなわち非 Windows では同時実行の検出は成立しない
// (tasks.md「未配線の契約」、lock_other.go の説明)。要件 4.6 の実体は
// Windows 受け入れテストで担保する。
func discardStaleTemp(outPath, tempPath string) error {
	// LockFileEx は読み取りまたは書き込みのアクセス権を要求するため O_RDWR で開く。
	f, err := os.OpenFile(tempPath, os.O_RDWR, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// 判定しようとした矢先に消えた。作成をやり直せばよい。
			return nil
		}
		return &ExitError{
			Code: ExitOutput,
			Err:  fmt.Errorf("出力先 %q の残存一時ファイル %q を確認できない: %w", outPath, tempPath, err),
		}
	}

	lockErr := tryLock(f)
	if lockErr == nil {
		_ = unlock(f)
	}
	// Windows では削除の前にハンドルを閉じる必要がある。
	_ = f.Close()

	if lockErr != nil {
		if errors.Is(lockErr, ErrLocked) {
			return &ExitError{
				Code: ExitOutput,
				Err: fmt.Errorf("出力先 %q は別のプロセスが処理中(一時ファイル %q がロックされている): %w",
					outPath, tempPath, lockErr),
			}
		}
		return &ExitError{
			Code: ExitOutput,
			Err:  fmt.Errorf("出力先 %q の残存一時ファイル %q をロックできない: %w", outPath, tempPath, lockErr),
		}
	}

	if err := os.Remove(tempPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return &ExitError{
			Code: ExitOutput,
			Err:  fmt.Errorf("出力先 %q の残存一時ファイル %q を削除できない: %w", outPath, tempPath, err),
		}
	}
	return nil
}

// WriteRecord は 1 レコードを 1 行として書き出す(RecordSink の実装)。
//
// compact のバイト列は呼び出しの間だけ有効という寿命契約があるため
// (convert.go の RecordSink のコメント)、保持せずその場で bufio.Writer へ渡す。
// bufio.Writer は与えられたスライスを内部バッファへ複写するか、バッファに入らない
// 大きさならその場で書き出すため、呼び出しを越えて参照が残ることはない。
//
// 改行は「区切り」として次のレコードの直前に書く。最終行の改行は Finalize が
// TrailingNewline に従って付ける(要件 3.5 / FR-14)。この配置により、レコードが
// 0 件のときはどちらの設定でも 1 バイトも書かれず、0 バイトのファイルが残る
// (要件 3.10 / FR-19)。
//
// BOM は決して書かない(要件 3.4 / FR-13)。レコードは生バイトのまま素通しするため、
// 値・数値字句・エスケープの変換も行わない(要件 3.7〜3.9)。
func (o *OutputManager) WriteRecord(compact jsontext.Value) error {
	if o.state != stateOpen {
		return &ExitError{
			Code: ExitInternal,
			Err:  fmt.Errorf("確定または中断済みの出力 %q へ書き込もうとした", o.cfg.Out),
		}
	}

	if o.wroteAny {
		if _, err := o.w.WriteString(o.newline); err != nil {
			return o.writeError(err)
		}
	}
	if _, err := o.w.Write(compact); err != nil {
		return o.writeError(err)
	}
	o.wroteAny = true
	return nil
}

// writeError は書き込み失敗を終了コード 4 のエラーへ写像する
// (design.md「Error Handling」: 出力書き込みエラー=4、stderr にパスを含む理由)。
func (o *OutputManager) writeError(cause error) error {
	return &ExitError{
		Code: ExitOutput,
		Err:  fmt.Errorf("出力先 %q への書き込みに失敗した(一時ファイル %q): %w", o.cfg.Out, o.tempPath, cause),
	}
}

// Finalize は書き込みを確定する。新規作成モードでは
// 最終行の改行 → Flush → Sync → unlock → Close → rename の順に進む。
//
// rename に成功した時点で、出力パスには完全な NDJSON だけが存在する。
// 途中で失敗した場合は一時ファイルを片付けたうえでエラーを返し、出力パスには
// 一切触れない(要件 4.2 / FR-21)。その場合は状態を「中断済み」にするので、
// 呼び出し元が defer で Abort を重ねても二重に片付けは走らない。
//
// 確定済み・中断済みの状態で呼ばれた場合は何もしない。
func (o *OutputManager) Finalize() error {
	if o.state != stateOpen {
		return nil
	}
	if o.mode == modeAppend {
		// 【継ぎ目】追記モードの確定(Flush→Sync→ジャーナル削除→unlock)は
		// タスク 3.3 / 3.4 が実装する。openOutput が追記モードの OutputManager を
		// 返さない現時点では到達しない。
		return errAppendNotWired("Finalize")
	}

	if err := o.commitNew(); err != nil {
		o.cleanupNew()
		o.state = stateAborted
		return err
	}
	o.state = stateCommitted
	return nil
}

// commitNew は新規作成モードの確定手順そのもの。失敗時の片付けは呼び出し元が行う。
func (o *OutputManager) commitNew() error {
	// 最終行の改行(要件 3.5 / FR-14)。レコードを 1 件も書いていないときは
	// 付けない。付けてしまうと空配列の出力が 0 バイトにならない(要件 3.10)。
	if o.wroteAny && o.cfg.TrailingNewline {
		if _, err := o.w.WriteString(o.newline); err != nil {
			return o.writeError(err)
		}
	}
	if err := o.w.Flush(); err != nil {
		return o.writeError(err)
	}
	// rename の前に内容をディスクへ確定させる(IMPL-14)。
	if err := o.file.Sync(); err != nil {
		return o.writeError(err)
	}

	// unlock の失敗は無視してよい。続く Close がロックを必ず解放するため、
	// ここで失敗を理由に処理全体を止めると、書き終えた内容を捨てることになる。
	_ = unlock(o.file)
	o.closed = true
	if err := o.file.Close(); err != nil {
		return o.writeError(err)
	}

	// ここで初めて出力パスに触れる。同一ディレクトリ(=同一ボリューム)の
	// 一時ファイルからの rename なので、原子的な置換として成立する(要件 4.2、NFR-13)。
	if err := os.Rename(o.tempPath, o.cfg.Out); err != nil {
		return &ExitError{
			Code: ExitOutput,
			Err:  fmt.Errorf("一時ファイル %q を出力先 %q へ置き換えられない: %w", o.tempPath, o.cfg.Out, err),
		}
	}

	// 【継ぎ目】design.md「output」は、新規モードの置換成功時に stale なジャーナル
	// (<出力名>.journal)を削除することを求めている。ジャーナルを作るのは追記モード
	// だけであり、その生成・解釈・削除を一括してタスク 3.4 が実装する。
	return nil
}

// Abort は書き込みを中断し、出力パスを実行前と同じ状態(新規作成モードでは
// 「存在しない」状態)へ戻す(要件 4.9 / FR-39)。
//
// 失敗しても報告経路を持たない(design.md の署名どおり戻り値なし)。best effort で
// 片付ける。確定済み・中断済みの状態で呼ばれた場合は何もしない。とくに
// 「Finalize 成功後に main の defer が Abort を呼ぶ」経路で確定済みの出力を
// 消さないことが重要。
func (o *OutputManager) Abort() {
	if o.state != stateOpen {
		return
	}
	o.state = stateAborted

	if o.mode == modeAppend {
		// 【継ぎ目】追記モードの中断(開始前サイズへの切り詰め → ジャーナル削除 →
		// unlock)はタスク 3.3 / 3.4 が実装する。現時点では到達しない。
		return
	}
	o.cleanupNew()
}

// cleanupNew は新規作成モードの後始末。一時ファイルを閉じて削除するだけで、
// 出力パスには触れない。
func (o *OutputManager) cleanupNew() {
	o.closeFile()
	if o.tempPath != "" {
		_ = os.Remove(o.tempPath)
	}
}

// closeFile はロックを解放してファイルを閉じる。二重に呼ばれても安全。
func (o *OutputManager) closeFile() {
	if o.file == nil || o.closed {
		return
	}
	o.closed = true
	_ = unlock(o.file)
	_ = o.file.Close()
}
