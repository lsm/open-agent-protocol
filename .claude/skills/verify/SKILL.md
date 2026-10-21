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
