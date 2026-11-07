# Native Rust ACP prompt admission and ordered inbound bridge

Follow shared Python/toolchain setup in SKILL.md, then run from the repository
root with a unique evidence directory:

```sh
python3 .claude/skills/verify/scripts/rust-acp-prompt.py --output-dir /tmp/oap-acp-prompt-<unique>
(cd rust && cargo test -p oap-adapter-acp --test prompt)
(cd rust && cargo test -p oap-adapter --test jsonrpc_client)
```

The public `prompt` consumer uses a real synthetic ACP stdio process with an
empty environment. It initializes the process, opens an opaque native session
through `PromptBridge::call`, observes write admission separately, and awaits a
typed prompt result. At queue capacity one, all 256 native notifications and
an intervening reverse request must appear in order before the result is
observed. The agent must read the actual reverse-request response. Captured
native frames must preserve session identity and text prompt content.

Seven cases cover ordered successful completion, dropping a quiet prompt,
closing during a quiet prompt, remote errors, malformed results, unmatched
response ids and EOF. Quiet cases verify nonblocking polling, bounded cleanup,
absent native PID and release of the dispatcher's callback owner before close
returns. Agents deliberately ignore EOF in the quiet cases. The verifier kills
its isolated process group in cleanup if any assertion fails. No TCP ports,
providers or credentials are used.

Focused tests hold an inbound handler and observe pending completion until it
returns, cancel an unfinished write-admission future, and join a panicking
handler. Shared RPC tests gate an actual writer and prove its observer stays
silent until writing completes, runs once and reports local write/admission
refusals. Existing bounded-queue and stall-closing coverage must keep passing.

This journey observes the native bridge layer. Normal OAP text submission is
covered by `rust-acp-submit.md`. Run the existing ACP foundation/session/model-switch journeys to
confirm those public OAP surfaces still work, the complete shared baseline and
Rust all-features suite, then inspect the baseline's isolated PTY reply/quit
proof. Remaining event mappings and the byte-for-byte adapter corpus retain
their approved later slices. Shared grandchild-held-pipe limitations remain applicable.
