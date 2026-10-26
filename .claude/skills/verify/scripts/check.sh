#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/../../../.."
artifacts=${OAP_VERIFY_ARTIFACTS:-$(mktemp -d "${TMPDIR:-/tmp}/oap-verify.XXXXXX")}
if [[ -z ${OAP_VERIFY_ARTIFACTS:-} ]]; then
    OAP_VERIFY_ARTIFACTS="$artifacts" bash "$0" 2>&1 | tee "$artifacts/check.log"
    exit "${PIPESTATUS[0]}"
fi
printf 'Artifacts: %s\n' "$artifacts"
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
run python3 .claude/skills/verify/scripts/go-usage.py --output-dir "$artifacts/go-usage"
run go test ./go/validation
run go test ./go/protocol
(
    cd rust
    run cargo test -p oap-validation --test manifest
    run cargo test -p oap-protocol --test payloads
    run cargo fmt --check
    run cargo clippy --all-targets --all-features -- -D warnings
    run cargo run -p nocomment -- --check
)
run zig build --build-file zig/build.zig -Doptimize=ReleaseSafe
run zig build --build-file zig/build.zig test-unit-validation -Doptimize=ReleaseSafe
run env OAP_OAPX_BIN="$PWD/zig/zig-out/bin/oapx" go test ./go/cmd/goap -run '^TestHubStdioBudgetsTheRawRequestMember$' -count=1 -v
run go run ./go/tools/nocomment --check
run node scripts/check-no-comments.mjs --check
run ./scripts/check-zig-patterns.sh
run ./scripts/check-no-test-litter.sh
run python3 .claude/skills/verify/scripts/acp-permission-publication.py --output-dir "$artifacts/acp-permission-publication"
run python3 .claude/skills/verify/scripts/stdio-overflow.py --output-dir "$artifacts/stdio-overflow"
run python3 .claude/skills/verify/scripts/sdk-command-result.py --output-dir "$artifacts/sdk-command"
run python3 .claude/skills/verify/scripts/python-sdk-usage.py --output-dir "$artifacts/python-sdk-usage"
run python3 .claude/skills/verify/scripts/python-sdk-negative-zero.py --output-dir "$artifacts/python-sdk-negative-zero"
run python3 .claude/skills/verify/scripts/rust-sdk-usage.py --output-dir "$artifacts/rust-sdk-usage"
run python3 .claude/skills/verify/scripts/go-sdk-usage.py --output-dir "$artifacts/go-sdk-usage"
run python3 .claude/skills/verify/scripts/ts-sdk-usage.py --output-dir "$artifacts/ts-sdk-usage"
run python3 .claude/skills/verify/scripts/usage-count-validation.py --output-dir "$artifacts/usage-count-validation"
run ./zig/zig-out/bin/oapx validate fixtures/valid/core-completed.json
run ./zig/zig-out/bin/oapx validate fixtures/valid/permission-resolved-in-the-refresh-window.json
run ./zig/zig-out/bin/oapx validate fixtures/valid/user-input-resolved-in-the-refresh-window.json
run python3 .claude/skills/verify/scripts/redelivery-wire.py --output-dir "$artifacts/redelivery-wire"
run python3 .claude/skills/verify/scripts/interaction-binding.py --output-dir "$artifacts/interaction-binding"
run python3 .claude/skills/verify/scripts/requester-identity.py --output-dir "$artifacts/requester-identity"
run python3 .claude/skills/verify/scripts/terminal-capture.py --output-dir "$artifacts/terminal-capture"
run python3 .claude/skills/verify/scripts/rust-deferred-captures.py --output-dir "$artifacts/rust-deferred-captures"
run python3 .claude/skills/verify/scripts/rust-snapshot-listing.py --output-dir "$artifacts/rust-snapshot-listing"
run python3 .claude/skills/verify/scripts/rust-reopen.py --output-dir "$artifacts/rust-reopen"
run python3 .claude/skills/verify/scripts/rust-refresh-window.py --output-dir "$artifacts/rust-refresh"
run python3 .claude/skills/verify/scripts/capability-refresh-revision.py --output-dir "$artifacts/capability-refresh-revision"
run python3 .claude/skills/verify/scripts/rust-control-tools.py --output-dir "$artifacts/rust-control"
run python3 .claude/skills/verify/scripts/rust-memory-trace.py --output-dir "$artifacts/rust-memory"
run python3 .claude/skills/verify/scripts/rust-provider-parity.py --output-dir "$artifacts/rust-provider"
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
