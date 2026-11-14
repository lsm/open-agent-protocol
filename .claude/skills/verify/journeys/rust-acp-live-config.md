# Rust ACP live config, state and model catalog

Follow shared isolation and mutex setup in SKILL.md. After the baseline builds
ReleaseSafe `oapx`, run from the repository root:

```sh
python3 .claude/skills/verify/scripts/rust-acp-live-config.py --output-dir "$EVIDENCE/live-config"
(cd rust && cargo test -p oxap-adapter-acp --test live_config)
```

The standalone public Session consumer and a Go reference run against the same
credential-free synthetic ACP agent with a one-entry inbound queue. The agent
sends `config_option_update`, then a reverse RPC. Its answered marker proves
the ordered dispatcher processed the update before the consumer reads state.
Every case closes and reaps its native child. Native wire in both directions,
raw consumer snapshots, wrapped OAP traces and validator outputs are retained.

Ten cases compare idle and active updates, replacement catalogs and option ids,
later model switches, removed model/reasoning options, placeholders, unknown
reasoning and case-folded aliases, current-value priority, multiple model
categories, wrong-session updates and malformed config. Comparison normalizes
only clock fields and the capability revision. Parsed native frames,
including the option id a later switch actually writes, must agree exactly.
All three validators must accept every trace built from the Rust results.

The focused tests also require an unchanged timestamp when only catalog values
change, a changed timestamp when recorded settings change, and no invented
stream events for config-only updates. A malformed active update still produces
`run.failed` with `acp_invalid_update`; idle malformed updates leave state alone.
The native PID check happens before assertions on recorded state.

Run the relevant existing session, model-switch, session-bridge, submit,
permission and cancellation journeys, complete baseline and Rust all-features
suite under the same mutex. Inspect the baseline's actual PTY fixture reply
and `/quit` exit. Native reload
is covered by `rust-acp-reload.md`. Opening config/reasoning is covered by `rust-acp-opening-config.md`; the byte
corpus by `rust-acp-corpus.md`.
No live providers, credentials, ports or served-parity claim are involved.
