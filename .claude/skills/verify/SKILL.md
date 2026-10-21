---
name: verify
description: Build and verify OAP validators and the oapx terminal surface on macOS before opening or merging coding changes.
---

# Verify OAP on this Mac

Run from the repository root. Read `CLAUDE.md` for architecture and additional
checks for the area changed. This baseline checks all three validators and the
local product surface; it does not prove live provider calls. Keep this skill
current in the same PR when a command changes or a user journey is added.

## Isolation and prerequisites

Use a fresh branch/worktree under `~/focus/worktrees/<name>`. Never run a bare
`git stash` in a shared checkout. After merging, stop everything you started and
remove your worktree with `git worktree remove <path>` from another checkout.

Use Go >=1.26, Zig 0.17.0, Rust/Cargo with rustfmt and clippy, Node, and Python 3.
Run `npm ci --ignore-scripts` at the root for the comment checker.
Check `go version`, `zig version`, `rustc --version` and `cargo --version`.
Use non-login shells: never source or read `~/.zshrc`, auth files, credential
stores, or keys. Do not print the environment. macOS has no stock `timeout`;
use Python `subprocess.run(..., timeout=...)` or bounded PTY waits and always
terminate/reap children in cleanup. Never run unattended GUI tests.

## Validators and guardrails

Run `bash .claude/skills/verify/scripts/check.sh` for the complete baseline
with a retained log and PTY artifacts under a fresh `/tmp/oap-verify.*` directory.
It stops on failure. Its checks are also listed below for focused reruns,
relative to the repository root:

```sh
go test ./go/validation
(cd rust && cargo test -p oap-validation --test manifest)
(cd rust && cargo fmt --check)
(cd rust && cargo clippy --all-targets --all-features -- -D warnings)
(cd rust && cargo run -p nocomment -- --check)
(cd zig && zig build -Doptimize=ReleaseSafe)
zig build --build-file zig/build.zig test-unit-validation -Doptimize=ReleaseSafe
go run ./go/tools/nocomment --check
node scripts/check-no-comments.mjs --check
./scripts/check-zig-patterns.sh
./scripts/check-no-test-litter.sh
```

Run tests from the repository root: pack tests resolve `fixtures/` from cwd.
The build is `zig build` in `zig/` (or use `--build-file zig/build.zig` from
root). Use ReleaseSafe for the binary and validation tests: Debug allocation
stack tracing makes the full manifest sweep very slow on this Mac.
Every tracked `.go`, `.zig`, `.ts`, and `.rs` has zero comments, including tests
and vendored sources; put rationale in prose, respecting only the enforcers'
existing toolchain-directive exceptions. Do not add an allowlist.

`fixtures/manifest.json` declares outcomes and exact diagnostic codes;
`fixtures/known-gaps.json` records implementation gaps. A passing manifest
suite does not claim every rule has been ported. For fixture-only changes,
Zig's test cache can reuse a previous result: always invoke the rebuilt binary
on each changed trace, even after `test-unit-validation` passes:

```sh
./zig/zig-out/bin/oapx validate fixtures/valid/core-completed.json
./zig/zig-out/bin/oapx validate <fixture>
```

Compare diagnostics and exit status to the manifest and known gaps. Positive
traces must exit 0; invalid traces must fail with the expected diagnostic, or
be explicitly reported as an existing gap. Check both a positive and a known
invalid trace when verifying the baseline. Zig CI can print `failed command`
while exiting 0: trust the actual command exit code, not that text. Conversely,
an unexpected code or inconclusive output is not a pass.

## Go usage consumer journey

After changing Go's token usage types or JSON mapping, run:

```sh
python3 .claude/skills/verify/scripts/go-usage.py --output-dir /tmp/oap-go-usage-<unique-name>
```

The script runs a standalone Go consumer importing the public `protocol.Usage`
type. Unknown counts must remain absent, independently measured zero counters
must remain `0`, and the maximum `uint64` total must survive a JSON round trip.
It retains the consumer source, input, output and stderr for inspection. No
provider, credentials or GUI is used. The baseline includes this journey.

## Interaction tool-binding consumer journey

After changing interaction binding (Decision 0055), run:

```sh
python3 .claude/skills/verify/scripts/interaction-binding.py --output-dir /tmp/oap-interaction-binding-<unique-name>
```

It replays the shared user-input fixture through `goap validate`, Rust's public
`validate` example and the rebuilt `oapx validate`. All three must reject the
resolution that moves `t1` to `t9` with only semantic `scope_mismatch` and exit 1.
Matching `t1`, omitting the optional resolution binding, and an unbound
interaction must pass with exit 0. Inputs and diagnostic JSON are retained.
The baseline runner includes this journey.

## Rust validator consumer journey

The Rust validator is a library. Its `validate` example calls the public
`Validator` API on trace files and prints the actual diagnostics as JSON;
exit 0 means valid, exit 1 means invalid. Replay the two shared refresh-window
fixtures, mutate each response's revision, and delay each response until after
a refresh that withdraws the feature to prove rejection is semantic:

```sh
python3 .claude/skills/verify/scripts/rust-refresh-window.py --output-dir /tmp/oap-rust-refresh-<unique-name>
```

The positive traces must report empty diagnostics and exit 0. Their mutated
responses must report `stale_capability_revision`, expected `v1`, actual `v2`,
and exit 1, with no decode or schema failures. A response delayed until after
refresh must instead report `unavailable_capability` under the refreshed
features while retaining the admitted revision echo, matching Go and Zig.
The script retains each input and result.
The baseline script runs this journey too, and validates the positive traces
with the rebuilt `oapx` binary.

Replay every steer fixture through both CLIs, the Rust `validate` example and
`goap validate --format json`:

```sh
python3 .claude/skills/verify/scripts/rust-steer-parity.py --output-dir /tmp/oap-rust-steer-<unique-name>
```

Every steer fixture not listed as a Rust known gap must draw the same code set
from both CLIs as the manifest declares, and exit 0 exactly when it draws none.
The script prints how many agreed and how many it skipped as listed gaps.

## Rust reopen consumer journey

After changing reopen state judgment, replay the shared idle, restored-queue,
unrecovered and running-run fixtures through the Rust public validator example,
Go CLI and rebuilt oapx:

```sh
python3 .claude/skills/verify/scripts/rust-reopen.py --output-dir /tmp/oap-rust-reopen-<unique-name>
```

Idle recovered sessions and recovered queued runs must pass. Missing recovery
and a running run must produce semantic `session_state_mismatch`, exit 1, in
all three. The baseline runner includes this journey and retains the JSON.
The focused Rust tests also cover the legacy active-run field, ordinary opens
and the carried-message exception. Mutation-check those tests in this worktree
with no concurrent Rust builds:

```sh
python3 .claude/skills/verify/scripts/rust-reopen.py --mutation-check --output-dir /tmp/oap-rust-reopen-mutations-<unique-name>
(cd rust && cargo test -p oap-validation --test reopen)
```

The optional mutation mode temporarily weakens six conditions in
`state/admissions.rs`, requires a test assertion failure for each (a compile
failure is not a kill), and restores the original source in `finally`.

The same example judges a `model-provider-core` trace with `--provider` first,
as `goap validate --provider` does; `oapx validate` routes a provider trace by
the profile it declares. Replay every provider fixture through all three:

```sh
python3 .claude/skills/verify/scripts/rust-provider-parity.py --output-dir /tmp/oap-rust-provider-<unique-name>
```

Rust and Go must report the same phase, code, envelope and pointer for every
diagnostic, and both they and Zig must draw exactly the codes the manifest
declares, exiting 0 only when it declares none. Build `oapx` first.

## Rust memory adapter consumer journey

The Rust adapter is a library too. Its `memory_trace` example opens a memory
session through the public `Adapter` and `Session` traits and prints the whole
exchange as an OAP trace. With `steer` it submits a run, steers it and resolves
both gates; with `compact` it asks an idle session for a focused compaction;
with `call` it opens the session providing a tool, then acknowledges and
resolves the call the script makes to it:

```sh
python3 .claude/skills/verify/scripts/rust-memory-trace.py --output-dir /tmp/oap-rust-memory-<unique-name>
```

Each trace must end in `run.completed` and pass the Rust, Go and Zig validators
(build `oapx` first). The steer trace must apply the steer right before the
input gate, and the compaction run must start and end its compaction in order.
With the steer exchange removed, all three must reject the trace with
`unmatched_steer`; with the compact exchange removed, all three must reject it
with `illegal_run_transition`. With the call's resolve exchanges removed, Go and
Zig must reject it with `illegal_tool_transition`; the Rust validator has not
ported the control-owned call rules yet, so the script does not check its
verdict there.

## Local oapx/TUI journey (PTY, no real credentials)

Run the bundled script, supplying a new artifact directory outside the worktree:

```sh
python3 .claude/skills/verify/scripts/mac-smoke.py --output-dir /tmp/oap-verify-smoke-<unique-name>
```

It copies the built binary into a temporary directory, clears the child
environment, supplies a fresh HOME and unique `OAPX_KEYCHAIN_SERVICE`, and
uses macOS `sandbox-exec` to deny network access, Security Mach services, and
reads under the real HOME. It reuses the existing PTY driver's terminal decoder
and bounded waits. It checks `oapx --help`, then observes welcome -> typed prompt
-> fixture-provider reply -> `/quit` with exit 0, saves help/transcript/screen
artifacts, kills/reaps the child on failure, and deletes temporary state.
Inspect `screen.txt` and the transcript for the actual rendered reply. The journey
also asserts real cursor coordinates and caret-column escapes for ASCII, CJK
wide characters and wrapped CJK input, then moves left over CJK, widens the PTY,
checks the new caret, types CJK in Zen mode, and checks cursor restoration on exit. `caret-*.txt` and
`resized.txt` retain the visible rows and real cursor coordinates. Run vendored
renderer escape/redraw/reflow tests with:

```sh
zig build --build-file zig/vendor/zigzag/build.zig test
```

A PTY cannot generate a terminal emulator's IME preedit. Real IME behaviour is
**unverified** until a human checks it in their own terminal:

1. Start the rebuilt `oapx`, enable a Chinese IME, and type a syllable without
   committing it; preedit should appear at the composer caret without scrolling.
2. Commit characters, fill/wrap an input row, move the caret left, and compose
   again; the frame should stay still and preedit should follow the caret.
3. Resize while a draft is present, compose and commit again, then `/quit`;
   check that the prompt and terminal cursor are restored correctly.

Do not remove the sandbox if it fails. HOME alone does not isolate macOS
Keychain; a unique Keychain service alone still permits file fallback and
`loadDefault` can import the fixed `Codex Auth` service. The general
`scripts/tui-pty-driver.py` rejects macOS for this reason; keep its guard intact.
Do not use login/logout, `/login`, `/model`, provider discovery or real SDK auth
smokes as substitutes. Live providers and real-binary SDK credential persistence
remain unverified on this Mac; the latter belongs on Linux CI.

## Evidence and merge

Put commands, exit codes, observed CLI diagnostics and PTY reply/quit evidence,
artifact paths, and explicit unverified surfaces in the PR description. For
broader Go changes run the Go checks from `CLAUDE.md`; for broader Rust changes
also run `cargo test --all-features` in `rust/`; for SDK/TUI changes run their
relevant CI groups and journeys. Do not describe lint or unit tests alone as
product proof.

Send Neo `lsm/open-agent-protocol#N` when the PR opens and at merge. Wait for the
review bot's approval of the latest head and green CI; address review threads,
reply, and resolve each addressed thread via `resolveReviewThread`. A new commit
needs fresh review. Manually run `gh pr merge N --squash` only after readiness;
never enable auto-merge. Clean up processes and the worktree after merging.
