# Rust ACP byte corpus

Follow shared setup and the verification mutex in SKILL.md. Run from the
repository root.

```sh
(cd rust && cargo test -p oap-adapter-acp --test corpus -- --nocapture)
(cd rust && cargo test -p oap-adapter-acp --all-features)
(cd rust && cargo test -p oap-serve --all-features --no-fail-fast)
python3 .claude/skills/verify/scripts/rust-acp-foundation.py --output-dir /tmp/oap-acp-foundation-<unique>
```

The corpus test spawns `tests/support/corpus_agent.py` once per prompted case
of the pinned corpus. The agent replays the case's native frames on its stdio,
so the adapter's real process transport carries every byte. The OAP trace must
equal the case's expected file byte for byte, including the catalog capability
revision, clocks and ids. Read the test's failure output: it prints the wanted
and actual trace for each case that differs.

Keep the signed red commit before the fix. On it the test fails because submit
demands a degradation opt-in Go's descriptor does not, because every event
carries a staged revision suffix, and because the process-exit case settles as
`acp_prompt_error` instead of `acp_transport_failure`.

The driver also checks the open-with-tool-sources native `mcpServers` array and
published sources, the replay-degradation `replay_after` resume, and that the
child knew every frame action. To see each check bite, empty the attachment's
`env` in `src/attachments.rs`, drop `update` from the agent's sent actions, or
start the journal's replay one sequence late: each must fail the test with its
own message, and the test must pass again once restored.

The only substitution is the process-exit message. Go's fake client names the
loss `fixture process exited`; a real pipe reports `EOF`. Every other byte is
compared as recorded. The session-reopen case has its own Go driver; `rust-acp-reopen-corpus.md`
covers it.

The serve suite runs the registry journey, which now requires the catalog
revision and Go's levels for submit, streaming and models, and admits a submit
with no opt-in. The foundation script checks the probe's full level table.
Keep the complete baseline and Rust format, clippy and comment checks.
