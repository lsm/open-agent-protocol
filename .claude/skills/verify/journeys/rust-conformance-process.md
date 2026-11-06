# Rust conformance process lifecycle

Use shared setup from `SKILL.md` and run the public consumer

```sh
python3 .claude/skills/verify/scripts/rust-conformance-wire.py --output-dir /tmp/oap-conformance-process-<unique-name>
```

Inspect `consumer.txt` for the rebuilt Rust endpoint's initialize and
capabilities responses, followed by exit 0 after stdin EOF. The same standalone
consumer must observe a soft receive timeout for a silent synthetic Python
endpoint, and an exit-budget error for one that closes stdout and stays alive.
Both cleanup paths must finish within three seconds. Require exit 0 and PASS.

Also run `cargo test --manifest-path rust/Cargo.toml -p oap-conformance`.
Its public process tests cover a full reader queue, output after a malformed
line, repeated close, and cleanup when a descendant keeps stdout open. The
descendant is explicitly stopped during test cleanup.

The script retains source, build logs and consumer output. Execution uses a
temporary HOME/data/config and synthetic memory/Python endpoints. The process
group is stopped on every script exit. Run the complete baseline too.
