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

Use the checkout/worktree assigned to the session and a fresh branch from the
fetched target for each task. Keep assigned checkouts across tasks. Remove a
task-created worktree when its task authorizes cleanup, after stopping anything
you started. Never run a bare `git stash` in a shared checkout.

Serialize verification on this machine with the shared `/tmp/oap-verify.lock`
directory mutex. Acquire it with `mkdir`, wait and retry while held, and release
only the directory acquired by this invocation in an exit trap. Keep the mutex
through the baseline, relevant journeys and broader language suites.

Use Go >=1.26, Zig 0.17.0, Rust/Cargo with rustfmt and clippy, Node, and Python >=3.11.
Check `python3 --version` before running the scripts. If it selects an older
Xcode Python, put a temporary `python3` launcher for an installed Python 3.11 or
newer first on PATH for the baseline and journeys, then remove the launcher.
Run `npm ci --ignore-scripts --include=dev` at the root and
`npm --prefix clients/ts ci --ignore-scripts --include=dev` for SDK/client
consumer journeys and the comment checker. `--include=dev` keeps TypeScript,
which the comment checker imports, when the shell sets `NODE_ENV=production`.
Check `go version`, `zig version`, `rustc --version` and `cargo --version`.
Use non-login shells: never source or read `~/.zshrc`, auth files, credential
stores, or keys. Do not print the environment. macOS has no stock `timeout`;
use Python `subprocess.run(..., timeout=...)` or bounded PTY waits and always
terminate/reap children in cleanup. Never run unattended GUI tests.

Keep the selected Python >=3.11 executable on `PATH` throughout verification.
If Go reports a missing build-cache archive, retain its stderr and rerun with
`GOCACHE` pointing at an owned, writable cache directory. Keep that cache for the
complete rerun, then remove it with the other owned temporary state.

Journeys that use `$EVIDENCE` write their separate artifact directories below a
fresh shared root. Set it once before running those journeys:

```sh
export EVIDENCE=$(mktemp -d "${TMPDIR:-/tmp}/oap-journeys.XXXXXX")
```

The Rust SDK usage consumer has an explicit 10-second fixture handshake budget.
Its first synthetic fixture delays startup by two seconds to verify that budget
and retains the complete consumer's elapsed time. SDK transport tests exercise
timeout behavior.

## Validators and guardrails

Run `bash .claude/skills/verify/scripts/check.sh` for the complete baseline
with a retained log and PTY artifacts under a fresh `/tmp/oap-verify.*` directory.
Set `TMPDIR` to a writable temporary directory when `/tmp` is restricted; the
runner respects it for retained artifacts and logs through an ordinary pipe.
It stops on failure. The runner also supports `bash check.sh` and `./check.sh`
from its scripts directory; `python3 .claude/skills/verify/scripts/check-invocations.py`
checks these entry points and preservation of the first failing check in the log. Its checks are also listed below for focused reruns,
relative to the repository root:

```sh
go test ./go/validation
(cd rust && cargo test -p oxap-validation --test manifest)
(cd rust && cargo fmt --check)
(cd rust && cargo clippy --all-targets --all-features -- -D warnings)
(cd rust && cargo run -p nocomment -- --check)
(cd zig && zig build -Doptimize=ReleaseSafe)
zig build --build-file zig/build.zig test-unit-validation -Doptimize=ReleaseSafe
zig build --build-file zig/build.zig test-unit-reactor -Doptimize=ReleaseSafe
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
./zig/zig-out/bin/oxap validate fixtures/valid/core-completed.json
./zig/zig-out/bin/oxap validate <fixture>
```

Compare diagnostics and exit status to the manifest and known gaps. Positive
traces must exit 0; invalid traces must fail with the expected diagnostic, or
be explicitly reported as an existing gap. Check both a positive and a known
invalid trace when verifying the baseline. Zig CI can print `failed command`
while exiting 0: trust the actual command exit code, not that text. Conversely,
an unexpected code or inconclusive output is not a pass.

## Journeys

Discover journey files by listing the directory from the repository root:

```sh
rg --files .claude/skills/verify/journeys -g '*.md' | sort
```

Read the listed files relevant to the area changed and follow their commands.
All journey commands run from the repository root. The baseline runner above
still runs its existing checks and consumer journeys.

Add a new journey as a new file in `journeys/`; never edit an index.
Use a distinct descriptive filename; no shared numbering or list needs updating.

## Documentation site

For site changes, also follow `journeys/docs-site.md`: build the isolated
VitePress package, check the published artifacts, and inspect the browser
journey under the repository base path. Hosted Pages smoke remains separate
until the administrator enables deployment.

## Evidence and merge

Keep commands, exit codes, observed CLI diagnostics, PTY reply/quit evidence,
artifact paths and unverified surfaces in the task evidence report. Summarize
observable verification briefly in the PR description, using portable commands
and public links for public repositories. For
broader Go changes run the Go checks from `CLAUDE.md`; for broader Rust changes
also run `cargo test --all-features` in `rust/`; for SDK/TUI changes run their
relevant CI groups and journeys. Do not describe lint or unit tests alone as
product proof.

Send Neo `lsm/oxap#N` when the PR opens and at merge. Wait for the
review bot's approval of the latest head and green CI; address review threads,
reply, and resolve each addressed thread via `resolveReviewThread`. A new commit
needs fresh review. The task's designated reviewer or merge service performs
the merge after its exact-head readiness and human-review gates. Clean up
processes and the worktree after merging.
