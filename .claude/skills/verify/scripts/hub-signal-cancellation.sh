#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/../../../.." && pwd)
proof_dir=${1:?pass an evidence directory}
repetitions=${2:-25}
binary=${OAP_OAPX_BIN:-$repo_root/zig/zig-out/bin/oapx}
go_cache=$(go env GOCACHE)
go_modules=$(go env GOMODCACHE)
probe_home=$(mktemp -d "${TMPDIR:-/tmp}/oap-hub-signal.XXXXXX")
trap 'rm -rf "$probe_home"' EXIT
mkdir -p "$proof_dir" "$probe_home/data" "$probe_home/config"
cd "$repo_root"
env -i PATH="$PATH" HOME="$probe_home" TMPDIR="${TMPDIR:-/tmp}" \
  XDG_DATA_HOME="$probe_home/data" XDG_CONFIG_HOME="$probe_home/config" \
  GOCACHE="$go_cache" GOMODCACHE="$go_modules" GOMAXPROCS=1 \
  OAP_OAPX_BIN="$binary" \
  go test -race ./go/cmd/goap \
    -run '^TestHubAddrFinishesTheRefusalAndStopsTheBodyWhenItsSignalArrives$' \
    -count="$repetitions" -v > "$proof_dir/signal.txt" 2>&1
printf 'PASS: refusal and signal observed with an unfinished body; owned daemons reaped\n'
