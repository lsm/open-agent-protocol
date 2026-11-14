# Bounded Rust ACP text-run engine

Follow shared setup in SKILL.md, then run from the repository root:

```sh
python3 .claude/skills/verify/scripts/rust-acp-text.py --output-dir /tmp/oap-text-engine-<unique>
(cd rust && cargo test -p oxap-adapter-acp --test text)
```

The script builds standalone public Rust and Go consumers. Rust combines
`PromptBridge` and `TextRun`; Go uses its public adapter/session APIs as the
reference. Both drive identical credential-free synthetic stdio agents. Native
queue capacity is one. Injected clocks and id generators make supported event
JSON compare exactly, including ids, timestamps, scope, sequences and payloads.
Native request frames must also agree. Every native PID must be reaped.

Cases cover Unicode and empty text, completion stop reasons, refusal and remote
errors, malformed/foreign/unknown updates, malformed config-option bodies,
invalid assistant chunks, bounded subscriber overflow and abandonment while an
agent ignores EOF. The overflow
case consumes after settlement and must receive the same bounded prefix and
explicit error on both peers. Abandonment retains the observed prefix. Focused
tests also drop the text-run producer, consume its retained prefix and require
the subscriber to finish immediately. Keep that ownership regression passing.

Multiple native message identities retain their separate OAP ids and text, and
the terminal response uses the last current message. Tool lifecycle is covered
by `rust-acp-tools.md`. The consumer retains both peers' outputs and verifies
the declared Rust failures. `run.cancelled` requires accepted host cancellation
evidence, which retains its approved later slice.

Go, Rust and Zig validate each complete Rust engine event trace with a test-host
submit prefix. The prefix uses the known test context and Go admission data,
while all Rust run events come from the real native bridge consumer. This proves
the standalone engine's event handoff. Normal Rust `Session.submit` is covered separately by `rust-acp-submit.md`.
Served adapter parity remains a later slice. Overflow and abandoned prefixes intentionally
lack a terminal and are checked as prefixes instead of full completed traces.

Run existing ACP opening/model/bridge journeys, the complete shared baseline
and Rust all-features suite too. Inspect the retained PTY fixture reply and
`/quit` exit. Use synthetic inputs. Shared helper grandchild-held-pipe limits
remain applicable. The script cleans up isolated process groups on failure.
