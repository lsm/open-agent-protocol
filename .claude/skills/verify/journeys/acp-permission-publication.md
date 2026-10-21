# ACP permission publication and prompt settlement

Build the ReleaseSafe `oapx` binary, then run from the repository root:

```sh
python3 .claude/skills/verify/scripts/acp-permission-publication.py --output-dir /tmp/oap-acp-permission-check
go test -race ./go/adapter/acp -run '^TestPromptSettlementResolvesAPermissionWhosePublisherIsPaused$' -count=1 -v
GOMAXPROCS=8 go test -race ./go/adapter/acp -run '^Test(PromptSettlementResolvesAPermissionWhosePublisherIsPaused|PendingPermissionsAreSettledInTheOrderTheyStarted)$' -count=300
```

The standalone public Go consumer starts a synthetic ACP agent over real stdio.
The agent requests two permissions and waits on a local Unix-socket barrier
until the consumer has observed both events before completing its prompt.
Both permissions must resolve once, before the run terminal. The generated OAP
trace must pass Go, Rust and Zig validation. Removing one resolution and
renumbering the remaining events must yield only
`pending_interaction_at_terminal` in all three. No live ACP harness or credentials
are used; the child receives an empty environment and is stopped during cleanup.
The script retains complete and mutated traces plus build and validator output.

The deterministic regression pauses the native permission handler after its
event reaches the public stream, while the fake native prompt settles. It must
still emit a resolution before terminality. The private per-session scheduling
hook only controls that boundary; assertions inspect the public stream and its
protocol validity. It failed in one run before moving publication bookkeeping
into the locked record callback. The stress command is additional coverage.
