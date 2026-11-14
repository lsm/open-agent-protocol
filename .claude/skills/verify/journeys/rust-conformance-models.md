# Rust conformance models and session listing

Use shared setup from `SKILL.md`, then run

```sh
python3 .claude/skills/verify/scripts/rust-conformance-wire.py --output-dir /tmp/oap-conformance-models-<unique-name>
cargo test --manifest-path rust/Cargo.toml -p oxap-conformance --test models --test runner
```

Inspect `consumer.txt` for a real model change on the rebuilt Rust memory
endpoint. The consumer first elects a model, then switches between two catalog
entries. It checks the switch responses and canonical state events, reconciles
state, and observes missing-model refusals naming the requested id. It also
checks the declared session-list outcome. Require thirteen passing named
checks, EOF exit 0 and PASS.
The listing probe must be absent from the collected trace.

Public API tests cover explicit model selection, unavailable/malformed catalogs,
advertised and unavailable lists, wrong acknowledgment/event/state/model-id
facts, idempotent switching and an empty selection. The shared synthetic
endpoint observes the real frames and rejects a session-scoped list request.

Source, build logs and output are retained. Runtime HOME/data/config are
temporary and the script cleans up its process group on every exit. Run the
complete baseline too.
