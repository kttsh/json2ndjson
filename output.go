package main

import (
	"bufio"
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

// output は出力ファイルの全ライフサイクル(排他・復旧・書き込み・確定・中断)を
// 単独で所有するコンポーネント(design.md「output(出力ファイル管理)」)。
//
// 【このファイルの実装範囲(タスク 3.2 / 3.3 / 3.4)】
// 新規作成モード(--append なし、タスク 3.2)、追記モードおよびプロセス内の
// ロールバック(--append、タスク 3.3)、ジャーナルによる強制終了後の復旧
// (要件 4.8 / NFR-07、タスク 3.4)を実装する。
//
// プロセス内のロールバックは「同一プロセス内の失敗 → 開始前サイズへの切り詰め」で
// あり、プロセスごと強制終了された場合には成立しない(切り詰めを行う主体が消えるため)。
// それを担保するのがジャーナルである。追記の 1 バイト目を書く前に開始前サイズを
// ディスクへ記録しておき、次回起動時に残っていれば記録サイズへ切り詰めてから始める。
//
// 【design.md との差異(実装上の必然)】
// design.md は Finalize の順序を「Flush→Sync→(rename|journal削除)→unlock」と
// 書いているが、新規モードでは rename の前に unlock と Close を行う。Windows の
// os.OpenFile は共有モードに FILE_SHARE_DELETE を含めないため、開いたままの
// ハンドルがあるとリネームが ERROR_SHARING_VIOLATION で失敗する。また Close 自体が
// ロックを解放するため、unlock を Close より後に置くことはできない。
// 「確定(rename)まで出力パスに触れない」という本質は保たれている。
//
// また design.md の Postconditions は「Abort 後、出力パスは…(新規: 不在)」と
// 括弧書きするが、新規モードの Abort は出力パスを削除しない。出典の FR-39 は
// 「FR-21 または FR-23 の状態に戻す」と定め、FR-21 は「失敗時に中途半端な
// ファイルを残さない」であって「存在しない状態」を要求していない。
// `--overwrite` の置換中に中断したとき出力パスを削除すると、実行前から存在した
// ユーザーのデータを失い、かえって FR-21 に反する。

// tempSuffix は一時ファイル名の接尾辞。出力名に付けるだけなので、一時ファイルは
// 必ず出力ファイルと同一ディレクトリに置かれる(要件 11.4 / NFR-13。同一ボリュームで
// あることが os.Rename の成立条件)。名前が決定的であることは、同時実行の検出
// (排他的作成+ロック)と、残骸の stale 判定の前提でもある(design.md「output」)。
const tempSuffix = ".tmp"

// journalSuffix はジャーナルファイル名の接尾辞(design.md「Data Models /
// ジャーナルファイル(復旧契約)」の `<出力パス>.journal`)。一時ファイルと同じく
// 出力名に付けるだけなので、必ず出力ファイルと同一ディレクトリに置かれる
// (要件 11.4 / NFR-13)。
//
// この名前と配置は design.md「Revalidation Triggers」に載る外部契約である
// (出力ディレクトリの退避・削除手順が影響を受ける)。変更は運用手順の再検証を要する。
const journalSuffix = ".journal"

// journalPrefix はジャーナル 1 行の接頭辞。内容は `size=<10進数>` + LF の 1 行に
// 限る(design.md「State Management」: 「ジャーナルは ASCII 1 行」)。
const journalPrefix = "size="

// journalMaxBytes はジャーナルとして受け付ける最大バイト数。
// 正規の内容は "size=" + 最大 19 桁(int64 の桁数)+ LF = 25 バイトであり、
// 余裕を見てもこの値を超えることはない。これを超えるファイルは中身を読むまでもなく
// ジャーナルの体を成しておらず、読み込み量の上限としても働く
// (ジャーナルのパスに巨大なファイルが置かれていても全部は読まない)。
const journalMaxBytes = 64

// journalOpenFlags はジャーナルを作成するときのフラグ。
//
// O_EXCL(排他的作成)であることが重要である。ここへ来る時点でジャーナルは
// 存在しないはずであり(追記の準備は必ず復旧を通る。復旧は残存ジャーナルを削除するか、
// 解釈できなければ終了コード 4 で停止する)、存在するなら不変条件が破れている。
// O_TRUNC で黙って上書きすると、「復旧が削除し忘れた」という重大な退行が、
// ディスク上の最終状態としては正しい実装と区別できなくなる。O_EXCL はそれを
// 作成の失敗として必ず露見させる(fail-closed)。
//
// O_APPEND は付けない。このファイルを Truncate することはないが、Windows で
// O_APPEND を付けたハンドルが FILE_WRITE_DATA を失う件(appendOpenFlags の注記 (2))と
// 同じ罠を再現しないための規律である。
const journalOpenFlags = os.O_WRONLY | os.O_CREATE | os.O_EXCL

// writeBufferSize は出力の書き込みバッファの大きさ(IMPL-15)。
// write システムコールの回数を減らすためのもので、レコード 1 件がこれを超えても
// bufio.Writer が直接書き出すため、メモリ使用量はレコード 1 件分+この値に漸近する。
const writeBufferSize = 64 << 10

// outputMode は出力ファイルの扱い方(design.md の状態遷移図の Init の分岐)。
type outputMode int

const (
	// modeNew は新規作成モード(--append なし)。一時ファイルへ書き、rename で置換する。
	modeNew outputMode = iota
	// modeAppend は追記モード(--append)。出力ファイル自身へ直接書き足す。
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
	// tempPath は新規作成モードの一時ファイルのパス。追記モードでは空文字列。
	tempPath string
	// baseSize は追記モードの「追記開始前のサイズ」(要件 4.4 / FR-23、4.9 / FR-39)。
	// 中断・確定失敗時はこのサイズへ切り詰めて元の状態に戻す。
	baseSize int64
	// padPending は追記モードで既存ファイルの末尾に改行を補う必要があるかどうか
	// (要件 4.5 / FR-24)。補完の改行は「追記の 1 バイト目」なので、開いた時点では
	// 書かず、最初のレコードの直前に書く(WriteRecord)。
	padPending bool
	// file は書き込み中のファイル。新規作成モードでは一時ファイル、
	// 追記モードでは出力ファイル自身を指す。
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
// 失敗した場合は OutputManager を返さず、新規作成モードでは出力パスにも一時ファイルにも
// 痕跡を残さない。追記モードでは要件 4.3 / FR-22 に従って準備の最初に出力ファイルを
// 作成するため、その後の段(ロック取得・サイズ取得・末尾判定)で失敗すると 0 バイトの
// ファイルが残りうる。これは「開始前サイズ 0」の状態であり、追記モードの Abort が
// 残す状態(要件 4.9 / FR-39)と一致する。
func openOutput(cfg *Config) (*OutputManager, error) {
	if cfg.Append {
		return openAppend(cfg)
	}
	return openNew(cfg)
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

// openAppend は追記モードの準備を行う(design.md「output」の「追記モード」)。
//
// 手順は「出力ファイルを開く(なければ作成)→ 出力ファイル自身をロック →
// 開始前サイズを記録 → 末尾 1 バイトで改行の有無を確認 → 書き込み位置を末尾へ合わせる」。
// 開く以降の段は prepareAppend が担う。
// 新規作成モードと違い、ロックの対象は一時ファイルではなく出力ファイル自身である
// (design.md「排他」: 「追記はロックを出力ファイル自身に取得」)。追記は出力パスへ
// 直接書き足すため、守るべき対象が出力ファイルそのものだからである。
func openAppend(cfg *Config) (*OutputManager, error) {
	f, err := os.OpenFile(cfg.Out, appendOpenFlags, 0o644)
	if err != nil {
		return nil, &ExitError{
			Code: ExitOutput,
			Err:  fmt.Errorf("追記先 %q を開けない: %w", cfg.Out, err),
		}
	}
	return prepareAppend(cfg, f, f)
}

// appendOpenFlags は追記先を開くときのフラグ。
//
// 【この組み合わせは 2 つの Windows 固有の制約に挟まれている。O_WRONLY へ
// 「単純化」してもならず、O_APPEND を「追記なのだから」と足してもならない。
// どちらも非 Windows では一切症状が出ない。】
//
// 前提: Go の syscall.Open は Windows で次のようにアクセス権を組み立てる
// (go1.27.0 の src/syscall/syscall_windows.go:372-394。1.25.1 から変更なし)。
//   - O_RDONLY→GENERIC_READ / O_WRONLY→GENERIC_WRITE / O_RDWR→両方
//   - O_CREAT があれば GENERIC_WRITE を追加
//   - O_APPEND があれば、O_TRUNC が無い限り GENERIC_WRITE を落とし
//     ("Remove GENERIC_WRITE unless O_TRUNC is set, in which case we need it
//     to truncate the file")、代わりに "Set all access rights granted by
//     GENERIC_WRITE except for FILE_WRITE_DATA"(FILE_APPEND_DATA |
//     FILE_WRITE_ATTRIBUTES | _FILE_WRITE_EA | STANDARD_RIGHTS_WRITE |
//     SYNCHRONIZE)だけを足す
//
// (1) O_RDWR は必須(O_WRONLY にしてはならない)。要件 4.5 / FR-24 の末尾 1 バイトの
// 読み取りに読み取り権限が要る。加えて Windows では、O_WRONLY に O_APPEND を併せると
// GENERIC_READ も GENERIC_WRITE も残らないハンドルになり、そのどちらかを要求する
// LockFileEx が ERROR_ACCESS_DENIED で失敗する。その値は ErrLocked へ写像されず
// 素通しされるため、要件 4.6 / FR-25 の同時実行排他が Windows 実機でのみ無言で壊れる。
//
// (2) O_APPEND は付けてはならない(「追記なのだから自然」という直感に反する)。
// 上記のとおり O_APPEND を付けたハンドルには FILE_WRITE_DATA が無い。ところが
// os.File.Truncate は Windows では poll.FD.Ftruncate → syscall.Ftruncate →
// SetFileInformationByHandle(FileEndOfFileInfo) に落ちており、これは FILE_WRITE_DATA を
// 要求する。したがって O_APPEND を付けると rollbackAppend のハンドル経由の切り詰めが
// Windows で必ず ERROR_ACCESS_DENIED になり、ロックを解放した後のパス経由の切り詰め
// (最後の手段)へ毎回落ちる。ロックが実体を持つ唯一の OS で、あらゆる中断が
// ロックの外側で切り詰めることになる。
//
// O_APPEND を落とした代わりに、開始前サイズの記録後に書き込み位置を明示的に合わせる
// (prepareAppend の Seek)。実行中はこのプロセスが排他ロックを保持する唯一の書き手で
// あり(要件 4.6 / FR-25)、O_APPEND の「書き込みのたびに末尾へ原子的に位置づける」
// 性質は必要ない。
//
// 非 Windows ではどちらの制約も症状が出ないため、TestAppendOpenFlagsAreWindowsSafe が
// この定数の値そのものを見張っている。
const appendOpenFlags = os.O_RDWR | os.O_CREATE

// prepareAppend は追記モードの準備のうち「開いたファイルを受け取ってからの段」を担う
// (ロック取得 → 開始前サイズの記録 → 末尾 1 バイトの確認 → 書き込み位置の設定)。
// 失敗した場合は f を必ずロック解放して閉じ、OutputManager を返さない。
//
// tail は末尾 1 バイトの読み取り先で、本番では f 自身を渡す。分けてあるのは、
// 読み取りが失敗したときにここが必ず停止する(=改行の補完を諦めて行を連結させない)
// ことをテストで固定できるようにするためである。needsNewlinePadding が
// io.ReaderAt を受けるのと同じ理由で、本番の経路には何の間接も増えない。
func prepareAppend(cfg *Config, f *os.File, tail io.ReaderAt) (*OutputManager, error) {
	if err := tryLock(f); err != nil {
		// 出力ファイルは自分が作ったとは限らないため、新規モードの一時ファイルと違い
		// 削除しない(実行前から存在したデータを失わないこと、要件 4.9)。
		_ = f.Close()
		if errors.Is(err, ErrLocked) {
			return nil, &ExitError{
				Code: ExitOutput,
				Err:  fmt.Errorf("追記先 %q は別のプロセスが処理中: %w", cfg.Out, err),
			}
		}
		return nil, &ExitError{
			Code: ExitOutput,
			Err:  fmt.Errorf("追記先 %q をロックできない: %w", cfg.Out, err),
		}
	}

	// 前回のプロセスが追記中に強制終了されていた場合の復旧(要件 4.8 / NFR-07)。
	// ロック取得の直後・開始前サイズの記録(下の Stat)より前でなければならない
	// 理由は recoverFromJournal の注記を参照。
	if err := recoverFromJournal(cfg.Out, f); err != nil {
		closeLocked(f)
		return nil, err
	}

	info, err := f.Stat()
	if err != nil {
		closeLocked(f)
		return nil, &ExitError{
			Code: ExitOutput,
			Err:  fmt.Errorf("追記先 %q のサイズを取得できない: %w", cfg.Out, err),
		}
	}
	baseSize := info.Size()

	pad, err := needsNewlinePadding(tail, baseSize)
	if err != nil {
		// ここで握り潰して pad=false に倒すと、末尾が改行で終わっていない追記先へ
		// 改行を補わずに書き足し、1 行に 2 レコードが乗った不正な NDJSON になる
		// (要件 4.5 / FR-24)。読めなかったのなら追記を始めてはならない。
		closeLocked(f)
		return nil, &ExitError{
			Code: ExitOutput,
			Err:  fmt.Errorf("追記先 %q の末尾 1 バイトを読めない: %w", cfg.Out, err),
		}
	}

	// O_APPEND を使わない(appendOpenFlags の注記 (2))ため、書き込み位置は明示的に
	// 合わせる必要がある。開いた直後のオフセットは 0 であり、ここを怠ると既存内容を
	// 先頭から上書きする。
	//
	// Seek(0, io.SeekEnd) ではなく Seek(baseSize, io.SeekStart) を使う。書き込みを
	// 始める位置と、ロールバックが戻す位置(baseSize)を同一の測定値に固定するためで
	// ある。末尾を測り直すと、Stat と Seek の間にサイズが変わった場合に「書き始めた
	// 位置」と「切り詰め先」がずれ、要件 4.4 / FR-23 の「追記開始前のサイズへ戻す」が
	// 穴あき(または他者の書き込みの巻き添え)になりうる。baseSize 基準なら
	// 「この実行が書いたバイトは必ず baseSize 以降にある」が構成上成り立つ。
	// タスク 3.4 のジャーナル復旧が切り詰めた直後でも、同じ理由で正しい位置になる。
	if _, err := f.Seek(baseSize, io.SeekStart); err != nil {
		closeLocked(f)
		return nil, &ExitError{
			Code: ExitOutput,
			Err:  fmt.Errorf("追記先 %q の書き込み位置を末尾へ移せない: %w", cfg.Out, err),
		}
	}

	// 追記の 1 バイト目を書く前に開始前サイズを記録して同期する(要件 4.8 / NFR-07)。
	// ここまでの段はファイルへ 1 バイトも書いておらず、最初の書き込みは WriteRecord
	// なので、この配置が design.md の不変条件「ジャーナルが sync される前に追記の
	// 1 バイト目を書かない」そのものになる。
	if err := writeJournal(cfg.Out, baseSize); err != nil {
		closeLocked(f)
		return nil, err
	}

	return &OutputManager{
		cfg:        cfg,
		mode:       modeAppend,
		state:      stateOpen,
		newline:    newlineOf(cfg),
		baseSize:   baseSize,
		padPending: pad,
		file:       f,
		w:          bufio.NewWriterSize(f, writeBufferSize),
	}, nil
}

// needsNewlinePadding は、サイズ size のファイルが改行で終わっていない
// (=追記の前に改行を補う必要がある)かを判定する(要件 4.5 / FR-24)。
//
// design.md「output」が定めるとおり、読むのは末尾の 1 バイトだけである。
// 追記先は運用上 GB 級まで育ちうるため、全体や末尾ブロックを読む実装は
// 実行のたびに追記先のサイズに比例した I/O を払うことになる。
//
// CRLF で終わるファイルも末尾 1 バイトは LF なので、この 1 バイトだけで
// LF・CRLF の双方を正しく判定できる。逆に CR 単独で終わるファイルは
// 行として閉じていないため補完が必要になる。
//
// io.ReaderAt を受けるのは、読み取り量そのものをテストで固定するためでもある
// (*os.File はこれを満たす)。
func needsNewlinePadding(r io.ReaderAt, size int64) (bool, error) {
	if size <= 0 {
		// 空のファイルには「終端していない直前の行」が存在しない。
		return false, nil
	}
	var last [1]byte
	n, err := r.ReadAt(last[:], size-1)
	if n == len(last) {
		// io.ReaderAt は要求量を満たしたうえで io.EOF を返すことを許している。
		// それを失敗として扱うと、正常な追記先を開けなくなる。
		return last[0] != '\n', nil
	}
	if err == nil {
		err = io.ErrUnexpectedEOF
	}
	return false, err
}

// journalPathOf はジャーナルのパスを返す(design.md「Data Models」)。
// OutputManager にフィールドとして持たせず毎回組み立てるのは、出力パスと
// ジャーナルパスの対応を 1 箇所に閉じ込め、取り違えや持ち回りの過程での
// 陳腐化を起こさないためである。
func journalPathOf(outPath string) string { return outPath + journalSuffix }

// writeJournal は追記開始前のサイズを記録したジャーナルを作成して同期する
// (要件 4.8 / NFR-07、design.md「output」の不変条件
// 「ジャーナルが sync される前に追記の 1 バイト目を書かない」)。
//
// 呼び出し位置は prepareAppend の末尾、すなわち「開始前サイズの記録・末尾改行の判定を
// 終え、まだ 1 バイトも書いていない」時点である。追記の 1 バイト目は最初の WriteRecord
// (末尾改行の補完、または最初のレコード)であり、タスク 3.3 が補完の改行を openAppend
// から WriteRecord へ意図的に遅らせたことで、この配置だけで不変条件が成立する。
//
// 途中で失敗した場合は作りかけのジャーナルを削除する。中途半端なジャーナルを残すと
// 次回実行が「解釈不能」として停止してしまうが(手動対処が必要になる)、この時点では
// まだ 1 バイトも追記していないため、ジャーナルが無いことが正しい状態
// (=ファイルは開始前サイズのまま)だからである。
func writeJournal(outPath string, size int64) error {
	p := journalPathOf(outPath)
	f, err := os.OpenFile(p, journalOpenFlags, 0o644)
	if err != nil {
		return &ExitError{
			Code: ExitOutput,
			Err: fmt.Errorf("追記先 %q のジャーナル %q を作成できない"+
				"(既に存在する場合、前回の実行の残骸が復旧で削除されていない): %w", outPath, p, err),
		}
	}

	if _, err := f.Write(fmt.Appendf(nil, "%s%d\n", journalPrefix, size)); err != nil {
		return journalWriteFailure(outPath, p, f, err)
	}
	// ここで同期しておかなければ、追記の 1 バイト目より前にジャーナルがディスクへ
	// 届いている保証がない(要件 4.8 の復旧はジャーナルの存在だけを手がかりにする)。
	if err := f.Sync(); err != nil {
		return journalWriteFailure(outPath, p, f, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(p)
		return &ExitError{
			Code: ExitOutput,
			Err:  fmt.Errorf("追記先 %q のジャーナル %q を閉じられない: %w", outPath, p, err),
		}
	}

	// ジャーナル本体を同期しても、それを指すディレクトリエントリが同期されるとは
	// 限らない(POSIX ではディレクトリの fsync が別に要る)。ジャーナルは
	// 「存在すること」自体が情報なので、エントリが失われると復旧できない。
	// 失敗しても続行する理由は syncContainingDir の注記を参照。
	syncContainingDir(p)
	return nil
}

// journalWriteFailure はジャーナルの書き込み・同期に失敗したときの後始末とエラー化。
func journalWriteFailure(outPath, journalPath string, f *os.File, cause error) error {
	_ = f.Close()
	_ = os.Remove(journalPath)
	return &ExitError{
		Code: ExitOutput,
		Err:  fmt.Errorf("追記先 %q のジャーナル %q を書き込めない: %w", outPath, journalPath, cause),
	}
}

// syncContainingDir は path を含むディレクトリを同期し、直前に作成した
// エントリの永続性を高める。
//
// 失敗しても報告しない。Windows の FlushFileBuffers はディレクトリハンドルを
// 受け付けず(os.File.Sync が失敗する)、この失敗を理由に追記を止めると
// 本来の目的である Windows 運用でツールが一切動かなくなる。エントリの同期は
// 「電源断でも復旧できる」ための上積みであって、本ツールが第一に想定する
// 強制終了(タイムアウトによるプロセス終了)では、書き込んだ内容は OS の
// ページキャッシュ経由で次のプロセスから必ず見える。
func syncContainingDir(path string) {
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// removeJournal はジャーナルを削除する。存在しない場合は成功として扱う
// (確定・中断の経路は何度呼ばれても安全でなければならない)。
func removeJournal(outPath string) error {
	p := journalPathOf(outPath)
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return &ExitError{
			Code: ExitOutput,
			Err: fmt.Errorf("出力先 %q のジャーナル %q を削除できない"+
				"(残すと次回の追記実行が誤ったサイズへ切り詰める): %w", outPath, p, err),
		}
	}
	return nil
}

// recoverFromJournal は、前回のプロセスが追記中に強制終了されていた場合に、
// 出力ファイルを前回の追記開始前の状態へ復旧する(要件 4.8 / NFR-07)。
//
// 呼び出し位置は「ロック取得の直後・開始前サイズの記録より前」でなければならない。
//   - ロック取得後: design.md「フロー上の決定事項」が定めるとおり、並行実行中の
//     他プロセスのジャーナルを誤って処理しないため(ロックを取る前に切り詰めると、
//     いま追記中の他プロセスのデータを破壊しうる)
//   - Stat より前: 残骸を切り詰める前にサイズを測ると、復旧前の誤ったサイズを
//     開始前サイズとして記録してしまう
//
// 切り詰めは呼び出し元が既に開いてロックしているハンドル f に対して行う。
// f は appendOpenFlags(O_APPEND を含まない)で開かれており、Windows でも
// ハンドル経由の Truncate が成立する(appendOpenFlags の注記 (2))。
// パス経由で開き直さないのは、ロックの外側で切り詰めないためでもある。
//
// 解釈できないジャーナルは自動判断せず終了コード 4 で停止し、手動調査に委ねる
// (design.md「Data Models」)。ジャーナルは「どこまでが利用者のデータか」を決める
// 唯一の手がかりであり、迷いのある値から切り詰め先を推測することは、復旧のつもりで
// データを破壊することに等しい。停止する場合はジャーナルも出力ファイルも変更しない。
func recoverFromJournal(outPath string, f *os.File) error {
	p := journalPathOf(outPath)
	raw, err := readJournal(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// 前回は正常に確定または中断している。復旧するものはない。
			return nil
		}
		return &ExitError{
			Code: ExitOutput,
			Err:  fmt.Errorf("追記先 %q のジャーナル %q を読めない: %w", outPath, p, err),
		}
	}

	size, err := parseJournalSize(raw)
	if err != nil {
		return &ExitError{
			Code: ExitOutput,
			Err: fmt.Errorf("追記先 %q のジャーナル %q を解釈できない(%w)。"+
				"自動で復旧せず停止する。内容を確認し、正しい状態へ戻してからジャーナルを削除すること", outPath, p, err),
		}
	}

	info, err := f.Stat()
	if err != nil {
		return &ExitError{
			Code: ExitOutput,
			Err:  fmt.Errorf("追記先 %q のサイズを取得できない(ジャーナル %q の復旧中): %w", outPath, p, err),
		}
	}
	if size > info.Size() {
		// 追記は「開始前サイズ」を記録するのだから、記録サイズがファイルより大きい状態は
		// 追記だけでは決して生じない。切り詰めではなく伸長になり NUL の穴を開けるため
		// Truncate は誤りであり、ジャーナルを無視して追記を続けるのは説明のつかない
		// 状態の上に書き足すことになる。破損と同じく手動調査に委ねる。
		return &ExitError{
			Code: ExitOutput,
			Err: fmt.Errorf("追記先 %q のジャーナル %q が記録するサイズ %d が現在のサイズ %d を上回る。"+
				"追記開始前のサイズとしてありえないため自動で復旧せず停止する", outPath, p, size, info.Size()),
		}
	}

	// 記録サイズと現在のサイズが等しい場合(ジャーナル作成直後の強制終了)も
	// そのまま切り詰める。結果は変わらないが、経路を分けないことで
	// 「復旧したつもりで何もしていない」分岐を作らずに済む。
	if err := f.Truncate(size); err != nil {
		return &ExitError{
			Code: ExitOutput,
			Err: fmt.Errorf("追記先 %q をジャーナル %q の記録サイズ %d へ切り詰められない: %w",
				outPath, p, size, err),
		}
	}
	// 切り詰めを確定させてからジャーナルを消す。順序を逆にすると、両者の間で
	// 強制終了された場合に「伸びたままのファイル」と「復旧の手がかりの消失」が
	// 同時に起こる。
	if err := f.Sync(); err != nil {
		return &ExitError{
			Code: ExitOutput,
			Err:  fmt.Errorf("追記先 %q の復旧後の切り詰めを同期できない(ジャーナル %q): %w", outPath, p, err),
		}
	}
	return removeJournal(outPath)
}

// readJournal はジャーナルの内容を読む。読み取り量は journalMaxBytes+1 バイトで
// 打ち切る(超過の検出に 1 バイトだけ余分に読む)。ジャーナルのパスに巨大なファイルが
// 置かれていても、それをすべてメモリへ載せることはない。
func readJournal(journalPath string) ([]byte, error) {
	f, err := os.Open(journalPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, journalMaxBytes+1))
}

// parseJournalSize はジャーナルの内容を追記開始前のサイズとして解釈する。
//
// 受け付けるのは `size=<10進数>` + LF の 1 行だけである(design.md
// 「State Management」/「Data Models」)。判定を厳格にしているのは、
// 少しでも解釈に迷う内容から切り詰め先を推測しないためである(データ保護を
// 自動復旧より優先する。design.md「Data Models」の設計判断)。したがって
// 空・接頭辞違い・前後の余分なバイト・複数行・改行なし・符号付き・先頭ゼロ・
// 10 進数以外・int64 に収まらない値・長すぎる内容は、すべて解釈不能として扱う。
func parseJournalSize(raw []byte) (int64, error) {
	if len(raw) > journalMaxBytes {
		return 0, fmt.Errorf("%d バイトを超えており 1 行の %q ではない", journalMaxBytes, journalPrefix+"<10進数>")
	}
	digits, ok := bytes.CutPrefix(raw, []byte(journalPrefix))
	if !ok {
		return 0, fmt.Errorf("%q で始まっていない", journalPrefix)
	}
	digits, ok = bytes.CutSuffix(digits, []byte("\n"))
	if !ok {
		return 0, errors.New("改行で終わっていない(書き込みが途中で切れた可能性がある)")
	}
	if len(digits) == 0 {
		return 0, errors.New("サイズの数字がない")
	}
	if len(digits) > 1 && digits[0] == '0' {
		// 先頭ゼロはこの実装が書くことのない字句であり、素性の知れないジャーナルである。
		return 0, errors.New("10 進数の先頭にゼロがある")
	}
	for _, b := range digits {
		if b < '0' || b > '9' {
			// 符号・空白・全角数字・改行を含む複数行はここで落ちる。
			return 0, fmt.Errorf("10 進数以外のバイト 0x%02X を含む", b)
		}
	}
	size, err := strconv.ParseInt(string(digits), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("int64 として解釈できない: %w", err)
	}
	return size, nil
}

// closeLocked はロックを解放してファイルを閉じる(OutputManager を組み立てる前の
// 失敗経路で使う)。失敗しても報告経路がないため best effort とする。
func closeLocked(f *os.File) {
	_ = unlock(f)
	_ = f.Close()
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
// (要件 3.10 / FR-19)。追記モードでも同じ理屈で、0 件の追記は既存ファイルを
// 1 バイトも変えない(末尾改行の補完も起きない)。
//
// 追記モードで補完の改行をここまで遅らせるのは、design.md「output」の不変条件
// 「ジャーナルが sync される前に追記の 1 バイト目を書かない」を満たすためでもある。
// 補完の改行こそが追記の 1 バイト目であり、openAppend で先に書いてしまうと、
// タスク 3.4 がジャーナルを openAppend の末尾に置いても不変条件が破れる。
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

	// 2 件目以降の区切りの改行と、追記モードで既存ファイルの末尾に補う改行
	// (要件 4.5 / FR-24)は、どちらも「行を分ける 1 個の改行」であり同じ扱いでよい。
	// padPending は最初のレコードの直前でしか参照されないため、消費後に戻す必要はない。
	if o.wroteAny || o.padPending {
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
	if o.tempPath != "" {
		return &ExitError{
			Code: ExitOutput,
			Err:  fmt.Errorf("出力先 %q への書き込みに失敗した(一時ファイル %q): %w", o.cfg.Out, o.tempPath, cause),
		}
	}
	// 追記モードは一時ファイルを介さず出力ファイル自身へ書く。
	return &ExitError{
		Code: ExitOutput,
		Err:  fmt.Errorf("出力先 %q への追記に失敗した: %w", o.cfg.Out, cause),
	}
}

// Finalize は書き込みを確定する。新規作成モードでは
// 最終行の改行 → Flush → Sync → unlock → Close → rename の順に、
// 追記モードでは 最終行の改行 → Flush → Sync → unlock → Close の順に進む。
//
// 新規作成モードでは rename に成功した時点で、出力パスには完全な NDJSON だけが存在する。
// 途中で失敗した場合は一時ファイルを片付けたうえでエラーを返し、出力パスには
// 一切触れない(要件 4.2 / FR-21)。追記モードで途中で失敗した場合は、出力ファイルを
// 追記開始前のサイズへ切り詰めてからエラーを返す(要件 4.9 / FR-39)。
// いずれの場合も状態を「中断済み」にするので、呼び出し元が defer で Abort を重ねても
// 二重に片付けは走らない。
//
// 確定済み・中断済みの状態で呼ばれた場合は何もしない。
func (o *OutputManager) Finalize() error {
	if o.state != stateOpen {
		return nil
	}

	var err error
	if o.mode == modeAppend {
		if err = o.commitAppend(); err != nil {
			o.rollbackAppend()
		}
	} else {
		if err = o.commitNew(); err != nil {
			o.cleanupNew()
		}
	}
	if err != nil {
		o.state = stateAborted
		return err
	}
	o.state = stateCommitted
	return nil
}

// finishLastLine は最終行の改行を必要に応じて書く(要件 3.5 / FR-14)。
//
// レコードを 1 件も書いていないときは付けない。新規作成モードでは付けてしまうと
// 空配列の出力が 0 バイトにならず(要件 3.10)、追記モードでは 0 件の追記が既存
// ファイルを書き換えてしまう。
func (o *OutputManager) finishLastLine() error {
	if !o.wroteAny || !o.cfg.TrailingNewline {
		return nil
	}
	if _, err := o.w.WriteString(o.newline); err != nil {
		return o.writeError(err)
	}
	return nil
}

// commitAppend は追記モードの確定手順そのもの。失敗時の切り詰めは呼び出し元が行う。
func (o *OutputManager) commitAppend() error {
	if err := o.finishLastLine(); err != nil {
		return err
	}
	if err := o.w.Flush(); err != nil {
		return o.writeError(err)
	}
	// 追記した内容をディスクへ確定させてからロックを手放す(IMPL-14)。
	if err := o.file.Sync(); err != nil {
		return o.writeError(err)
	}

	// ジャーナルを削除して「この追記は確定した」ことを記録する(要件 4.8 / NFR-07)。
	// 位置は Sync の後・ロック解放の前でなければならない。
	//   - Sync の後: 追記した内容がディスクへ届く前にジャーナルを消すと、その間の
	//     強制終了で「不完全な追記」と「復旧の手がかりの消失」が同時に起こる
	//   - ロック解放の前: 解放後は別プロセスが同じ出力ファイルのロックを取って追記を
	//     始め、自分のジャーナルを作りうる。そこで削除すると他プロセスのジャーナルを
	//     消すことになる
	//
	// なお Sync と削除の間で強制終了された場合、次回実行はディスクへ届いている
	// この追記を開始前サイズへ切り詰める。呼び出し元は結果行も終了コード 0 も
	// 受け取っていない以上、この実行は失敗として再実行される。追記を残すほうが
	// 再実行時の重複を生むため、切り詰められるのが正しい挙動である(要件 10.1 の冪等性)。
	if err := removeJournal(o.cfg.Out); err != nil {
		return err
	}

	// unlock の失敗は無視してよい。続く Close がロックを必ず解放するため、
	// ここで失敗を理由に処理全体を止めると、書き終えた内容を捨てることになる。
	_ = unlock(o.file)
	o.closed = true
	if err := o.file.Close(); err != nil {
		return o.writeError(err)
	}
	return nil
}

// commitNew は新規作成モードの確定手順そのもの。失敗時の片付けは呼び出し元が行う。
func (o *OutputManager) commitNew() error {
	if err := o.finishLastLine(); err != nil {
		return err
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

	// 置換に成功したので、前回の追記が残した stale なジャーナルを削除する
	// (design.md「output / 復旧」)。残したままにすると、次の `--append` 実行が
	// 「前回の追記が未確定」と解釈し、置き換えたばかりのファイルを無関係な記録サイズへ
	// 切り詰めてしまう。削除できない場合に黙って成功させると、その時限装置を残したまま
	// 終了コード 0 を返すことになるため、パスを添えて終了コード 4 で報告する。
	return removeJournal(o.cfg.Out)
}

// Abort は書き込みを中断し、出力パスを**実行前と同じ状態**へ戻す(要件 4.9 / FR-39)。
// 新規作成モードでは一時ファイルを削除するだけで、出力パスには一切触れない。
// 出力パスが実行前から存在しなければ不在のままであり、`--overwrite` で既存ファイルを
// 置換する途中だった場合は既存ファイルがそのまま残る。
// 追記モードでは出力ファイルを追記開始前のサイズへ切り詰める(要件 4.4 / FR-23)。
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
		o.rollbackAppend()
		return
	}
	o.cleanupNew()
}

// rollbackAppend は追記モードの後始末。出力ファイルを追記開始前のサイズへ
// 切り詰めてから閉じる(要件 4.4 / FR-23、4.9 / FR-39)。
//
// バッファに残った未書き出しのバイトは破棄する(Flush しない)。中断は「書きかけを
// 無かったことにする」処理であり、ここで書き足す理由はない。
//
// 出力ファイル自体は削除しない。`--append` で新規作成した場合の開始前サイズは 0 で
// あり、0 バイトへの切り詰めが要件 4.9 の定める復元そのものである。削除は「自分が
// 作ったのか、開いた瞬間に他者が作ったのか」を区別できないまま他者のデータを消しうる
// (新規作成モードの Abort が出力パスに触れないのと同じ判断)。
func (o *OutputManager) rollbackAppend() {
	truncated := false
	if o.file != nil && !o.closed {
		// ハンドル経由で切り詰める。これが正規の経路であり、ロックを保持したまま
		// 完了するため他プロセスに割り込まれない。
		//
		// これが Windows でも成り立つのは、appendOpenFlags が O_APPEND を含まないから
		// である。O_APPEND を付けたハンドルには FILE_WRITE_DATA が無く
		// (syscall_windows.go:386-395)、os.File.Truncate が使う
		// SetFileInformationByHandle(FileEndOfFileInfo) はそれを要求するため、
		// ここが必ず ERROR_ACCESS_DENIED になって下のパス経由へ落ちてしまう。
		// appendOpenFlags とこの一行は一組の不変条件であり、前者は
		// TestAppendOpenFlagsAreWindowsSafe が見張っている。
		if err := o.file.Truncate(o.baseSize); err == nil {
			truncated = true
		}
	}
	if truncated {
		// 切り詰めが済んでから、かつロックを保持したままジャーナルを削除する
		// (要件 4.8 / NFR-07)。
		//   - 切り詰めの後: 先に消すと、両者の間で強制終了された場合に伸びたままの
		//     ファイルと復旧の手がかりの消失が同時に起こる
		//   - ロック保持中: 解放後は別プロセスが自分のジャーナルを作りうるため、
		//     そこでの削除は他プロセスのジャーナルを消す危険がある
		// Abort は報告経路を持たない best effort であり、削除の失敗は伝えられない。
		// 失敗して残った場合でも、次回実行は開始前サイズへ切り詰めるだけなので
		// 状態は一致する(ジャーナルの記録サイズ=いま戻したサイズ)。
		_ = removeJournal(o.cfg.Out)
	}
	// Windows ではハンドルを閉じるまでロックが残る。以降の os.Truncate のためにも
	// 先に閉じる。
	o.closeFile()

	if !truncated {
		// 【最後の手段。正規の経路ではない】ハンドルが使えない状態(確定手順の途中で
		// 閉じた後の失敗、書き込み失敗でディスクリプタが無効になった場合など)でも、
		// 開始前サイズへ戻すことは諦めずパス経由で試みる。
		//
		// ただしこの切り詰めは上の closeFile でロックを解放した後に走る。すなわち
		// 別プロセスが同じ出力ファイルのロックを取得して追記を始めた直後であれば、
		// その追記ごと baseSize へ切り詰めてしまう窓がある。ハンドル経由の切り詰めを
		// 何としても成立させ、ここへ落ちないようにしておくことが重要なのはこのためで
		// ある。それでも試みるのは、「開始前サイズへ戻らないまま終わる」ほうが
		// 要件 4.4 / FR-23・4.9 / FR-39 に対して確実に悪いからである。
		//
		// Abort は戻り値を持たない best effort の処理であり(design.md の署名どおり)、
		// ここで失敗しても報告経路はない。プロセスごと強制終了された場合の復旧は、
		// タスク 3.4 のジャーナルが担う。
		_ = os.Truncate(o.cfg.Out, o.baseSize)

		// パス経由の切り詰めを試みた後にもジャーナルを削除する。ここはロックの外側
		// だが、削除しないまま終わると次回実行がこの(既に戻したはずの)サイズを
		// 手がかりに復旧を試みることになる。記録サイズは戻し先と同一なので、
		// 万一残っても実害はない。
		_ = removeJournal(o.cfg.Out)
	}
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
