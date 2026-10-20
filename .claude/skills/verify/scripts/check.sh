#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/../../../.."
artifacts=$(mktemp -d /tmp/oap-verify.XXXXXX)
printf 'Artifacts: %s\n' "$artifacts"
exec > >(tee "$artifacts/check.log") 2>&1
run() {
    printf '\nRUN:'
    printf ' %q' "$@"
    printf '\n'
    local code=0
    "$@" || code=$?
    printf 'EXIT: %s\n' "$code"
    return "$code"
}
run go version
run zig version
run rustc --version
run cargo --version
run go test ./go/validation
(
    cd rust
    run cargo test -p oap-validation --test manifest
    run cargo fmt --check
    run cargo clippy --all-targets --all-features -- -D warnings
    run cargo run -p nocomment -- --check
)
run zig build --build-file zig/build.zig -Doptimize=ReleaseSafe
run zig build --build-file zig/build.zig test-unit-validation -Doptimize=ReleaseSafe
run go run ./go/tools/nocomment --check
run node scripts/check-no-comments.mjs --check
run ./scripts/check-zig-patterns.sh
run ./scripts/check-no-test-litter.sh
run ./zig/zig-out/bin/oapx validate fixtures/valid/core-completed.json
run ./zig/zig-out/bin/oapx validate fixtures/valid/permission-resolved-in-the-refresh-window.json
run ./zig/zig-out/bin/oapx validate fixtures/valid/user-input-resolved-in-the-refresh-window.json
run python3 .claude/skills/verify/scripts/rust-refresh-window.py --output-dir "$artifacts/rust-refresh"
run python3 .claude/skills/verify/scripts/rust-memory-trace.py --output-dir "$artifacts/rust-memory"
run python3 .claude/skills/verify/scripts/rust-steer-parity.py --output-dir "$artifacts/rust-steer"
printf '\nRUN: oapx validate fixtures/schema-invalid/missing-envelope-id.json (expect exit 1, schema_invalid)\n'
set +e
./zig/zig-out/bin/oapx validate fixtures/schema-invalid/missing-envelope-id.json > "$artifacts/invalid.txt" 2>&1
code=$?
set -e
cat "$artifacts/invalid.txt"
printf 'EXIT: %s\n' "$code"
test "$code" -eq 1
grep -q schema_invalid "$artifacts/invalid.txt"
run python3 .claude/skills/verify/scripts/mac-smoke.py --output-dir "$artifacts/pty"
printf '\nPASS: complete local baseline; live provider calls unverified.\n'
