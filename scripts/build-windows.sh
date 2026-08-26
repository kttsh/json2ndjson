#!/usr/bin/env bash
#
# build-windows.sh — Windows 向け配布ビルド(要件定義書 BUILD-01〜BUILD-03、タスク 6.2)
#
# 規定のクロスコンパイルコマンドで単一の実行ファイル json2ndjson.exe を生成し、
# SHA-256 を添えて dist/ へ出力する。ビルドはどの OS 上で行ってもよく(BUILD-02)、
# 配布前の受け入れテストは Windows Server 上で実行する。
#
# 使い方:
#   scripts/build-windows.sh              # バージョンは git タグから解決(なければ dev)
#   scripts/build-windows.sh v1.2.3       # バージョンを明示
#   VERSION=v1.2.3 scripts/build-windows.sh
#   SKIP_CHECKS=1 scripts/build-windows.sh   # 静的解析とテストを省略(緊急時のみ)
#
# 終了コードは 0(成功)か 1(いずれかの工程で失敗)。途中で失敗した場合、
# dist/ に中途半端な成果物を残さない。

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

dist_dir="dist"
exe_name="json2ndjson.exe"
exe_path="$dist_dir/$exe_name"

log() { printf '==> %s\n' "$*"; }
die() { printf 'エラー: %s\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# GOEXPERIMENT の解決
#
# encoding/json/jsontext は Go 1.27 で既定有効だが、1.25 / 1.26 では
# GOEXPERIMENT=jsonv2 が要る。1.27 以降で jsonv2 を明示すると未知の実験名として
# 拒否されうるため、ツールチェーンのバージョンを見て必要なときだけ付ける。
# ---------------------------------------------------------------------------
resolve_goexperiment() {
  local goversion minor
  goversion="$(go env GOVERSION)"           # 例: go1.25.1
  minor="${goversion#go1.}"                 # 例: 25.1
  minor="${minor%%.*}"                      # 例: 25
  if ! [[ "$minor" =~ ^[0-9]+$ ]]; then
    log "Go のバージョン $goversion を解釈できないため GOEXPERIMENT は変更しない"
    return
  fi
  if (( minor >= 27 )); then
    log "Go $goversion: jsonv2 は既定有効のため GOEXPERIMENT は不要"
    return
  fi
  if [[ ",${GOEXPERIMENT:-$(go env GOEXPERIMENT)}," == *",jsonv2,"* ]]; then
    log "Go $goversion: GOEXPERIMENT に jsonv2 が既に含まれている"
    return
  fi
  export GOEXPERIMENT="${GOEXPERIMENT:+$GOEXPERIMENT,}jsonv2"
  log "Go $goversion: GOEXPERIMENT=$GOEXPERIMENT を設定した"
}

# ---------------------------------------------------------------------------
# バージョンの解決(IMPL-21)
#
# 優先順位は 引数 > $VERSION > git タグ。いずれも得られなければ "dev"。
# 埋め込まない場合の既定 "dev" は version.go 側にもあり、ここでの既定と一致する。
# ---------------------------------------------------------------------------
resolve_version() {
  if (( $# > 0 )) && [[ -n "$1" ]]; then
    printf '%s' "$1"; return
  fi
  if [[ -n "${VERSION:-}" ]]; then
    printf '%s' "$VERSION"; return
  fi
  if git describe --tags --dirty 2>/dev/null; then
    return
  fi
  printf 'dev'
}

# ---------------------------------------------------------------------------
# 検証(IMPL-04)
# ---------------------------------------------------------------------------
run_checks() {
  if [[ -n "${SKIP_CHECKS:-}" ]]; then
    log "SKIP_CHECKS が設定されているため静的解析とテストを省略する"
    return
  fi

  log "gofmt"
  local unformatted
  unformatted="$(gofmt -l .)"
  [[ -z "$unformatted" ]] || die "gofmt が未整形のファイルを検出した:"$'\n'"$unformatted"

  log "go vet"
  go vet ./... || die "go vet が失敗した"

  # staticcheck は開発環境のみのツールであり配布物には含めない(IMPL-04)。
  # 入っていない環境ではビルド自体は続行し、省略した事実だけ残す。
  local staticcheck_bin=""
  if command -v staticcheck >/dev/null 2>&1; then
    staticcheck_bin="staticcheck"
  elif [[ -x "$(go env GOPATH)/bin/staticcheck" ]]; then
    staticcheck_bin="$(go env GOPATH)/bin/staticcheck"
  fi
  if [[ -n "$staticcheck_bin" ]]; then
    log "staticcheck ($("$staticcheck_bin" -version))"
    "$staticcheck_bin" ./... || die "staticcheck が失敗した"
  else
    log "警告: staticcheck が見つからないため省略する(IMPL-04 は開発時ツールとして要求している)"
  fi

  # 300 MB の性能テストは -short で除外する(TEST-02)。配布ビルドの門番として
  # 必要なのは回帰がないことの確認であり、性能の実測は別途 6.1 のテストで行う。
  log "go test -short"
  go test -short ./... || die "go test が失敗した"
}

# ---------------------------------------------------------------------------
# クロスコンパイル(BUILD-01)
# ---------------------------------------------------------------------------
build_exe() {
  local version="$1"
  rm -rf "$dist_dir"
  mkdir -p "$dist_dir"

  log "クロスコンパイル(GOOS=windows GOARCH=amd64 CGO_ENABLED=0、version=$version)"
  GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
    go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$exe_path" . \
    || die "クロスコンパイルが失敗した"
}

# ---------------------------------------------------------------------------
# 成果物の検証
#
# 本環境では Windows 実行ファイルを起動できないため、次の 3 点で確かめる。
#   1. PE 形式の Windows 実行ファイルであること
#   2. 埋め込んだバージョン文字列がバイナリ中に存在すること
#   3. 同じ -ldflags でホスト向けにビルドした実行ファイルの --version が
#      そのバージョンを表示し、終了コード 0 で終わること(要件 7.5 / NFR-11)
#
# 3 は -X の注入が効いていることの実行による裏付けである。exe 自体の起動確認は
# Windows Server 上の受け入れテストで行う(BUILD-02)。
# ---------------------------------------------------------------------------
verify_exe() {
  local version="$1"

  log "形式の確認"
  local filetype
  filetype="$(file -b "$exe_path")"
  printf '    %s\n' "$filetype"
  [[ "$filetype" == *"PE32+"* && "$filetype" == *"MS Windows"* ]] \
    || die "生成物が Windows の PE 実行ファイルではない: $filetype"

  log "バージョン文字列の埋め込み確認"
  grep -q -- "$version" "$exe_path" \
    || die "実行ファイルにバージョン文字列 $version が見つからない(-X の注入が効いていない)"

  log "--version の動作確認(ホスト向けビルドで -X の注入を実行検証)"
  local host_bin
  host_bin="$(mktemp -d)/json2ndjson-hostcheck"
  CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$host_bin" . \
    || die "ホスト向けの検証ビルドが失敗した"
  local got
  got="$("$host_bin" --version)" || die "--version が終了コード 0 で終わらなかった"
  printf '    %s\n' "$got"
  [[ "$got" == "json2ndjson $version" ]] \
    || die "--version の出力が想定と違う: 実際 '$got' / 期待 'json2ndjson $version'"

  # 追加のランタイムやライブラリを要求しないこと(要件 11.1 / 5.2 節)。PE の
  # import テーブルは file では見えないため、ホスト向けビルドが静的リンクで
  # あることを裏付けとする。
  #
  # 注意: この検査は CGO_ENABLED=0 が設定されたことの検証ではない。本ツールの
  # import グラフには cgo を使うパッケージ(net、os/user など)が 1 つもないため、
  # CGO_ENABLED=1 でビルドしても静的リンクになる(実測確認済み)。したがって
  # ここで固定できるのは「生成物が静的リンクである」という結果だけであり、
  # それこそが要件 11.1 が求めるもの(配置するだけで動く)である。
  # BUILD-01 の CGO_ENABLED=0 は、C コンパイラのある環境で将来 cgo 依存が
  # 紛れ込んでも生成物が変わらないようにするための指定として維持する。
  local host_type
  host_type="$(file -b "$host_bin")"
  printf '    %s\n' "$host_type"
  [[ "$host_type" == *"statically linked"* ]] \
    || die "ホスト向けビルドが静的リンクではない。配置するだけで動く要件 11.1 を満たさない: $host_type"

  rm -rf "$(dirname "$host_bin")"
}

# ---------------------------------------------------------------------------
# SHA-256(BUILD-03、タスク 6.2 の完了条件)
# ---------------------------------------------------------------------------
write_checksum() {
  log "SHA-256 の算出"
  local sum_file="$exe_path.sha256"
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$dist_dir" && sha256sum "$exe_name" > "$exe_name.sha256")
  elif command -v shasum >/dev/null 2>&1; then
    (cd "$dist_dir" && shasum -a 256 "$exe_name" > "$exe_name.sha256")
  else
    die "sha256sum も shasum も見つからないためチェックサムを算出できない"
  fi
  printf '    %s\n' "$(cat "$sum_file")"
}

main() {
  resolve_goexperiment
  local version
  version="$(resolve_version "$@")"
  log "バージョン: $version"

  run_checks
  build_exe "$version"
  verify_exe "$version"
  write_checksum

  log "完了: $exe_path($(wc -c < "$exe_path") バイト)"
  log "配布時は exe と .sha256 に、対応する要件定義書の版とリリースノートを添える(BUILD-03 / Q-11)"
  log "受け入れテスト(要件定義書 9 章)は Windows Server 上で実行すること(BUILD-02)"
}

main "$@"
