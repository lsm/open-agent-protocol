# Rust ACP per-open MCP attachments

Follow shared prerequisites and the mkdir mutex in SKILL.md. After the baseline
builds ReleaseSafe oapx, run from the repository root:

```sh
python3 .claude/skills/verify/scripts/rust-acp-attachments.py --output-dir "$EVIDENCE/attachments"
(cd rust && cargo test -p oap-adapter-acp --test attachments --test attachment_cases)
```

The normal public Rust Adapter/Session and Go reference drive identical
synthetic native stdio agents. They never execute the MCP commands they carry.
Use only the checked-in synthetic operator values, empty/default explicit child
environments, and queue capacity one. No provider, real MCP server or port is
used. The captured open-with-tool-sources array must match its original expected
MCP servers exactly, including an allowlisted name, dropped unexposed name and
literal value. Keep its signed failing admission/open test before the fix.

Twenty-nine cases cover configured/attached ordering, namespace conflicts,
empty ids/kinds/commands, process transport limits, duplicate environment names,
missing/bare/literal/empty/escaped values, last configured value, opaque protocol
metadata, validation priority and repeated independent opens. Compare complete
typed control metadata, standard error categories, returned state and both
parsed native wire directions. Normalize only clocks/staged revision; retain
raw diagnostic wording. Go wraps missing-command InvalidResolution with source
context, while Rust's shared error is a unit variant; do not claim those raw
strings are identical. Rust admission must agree with its actual open outcome,
and each refused source must leave no native PID.

New, load and resume receive the same complete server array. Reload fixtures
send 320 notifications and a reverse request before responding, proving that
source forwarding retains the existing bounded drain. Cancellation cases wait
until the peer records the actual MCP-bearing request, drop/cancel open, and
require every native PID reaped before returning, without publishing a Session.
Public state/probe must expose only descriptor projections; later opens and the
global probe must not acquire a previous open's sources.

Thirteen successful state exchanges across twelve cases must pass all three
validators. Fourteen refusal cases are preflight/no-startup evidence, and three
cancellation cases are real native cleanup evidence; do not label them state
validation passes when no state was published. Retain raw input, result,
both wire directions, native PIDs and validator output in the evidence folder.

Run all prior ACP journeys including rust-acp-journal.md, the complete baseline
and Rust all-features under the same mutex. Read the actual PTY reply and /quit
exit. The current revision is -rust-attachments; deterministic clocks/ids and
the full byte corpus remain the next approved slice. Shared process ownership
limits apply. This is not a C8 served-parity hand-off.
