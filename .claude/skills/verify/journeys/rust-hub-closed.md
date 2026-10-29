## Rust hub closed-session lifecycle

Use the shared toolchain setup in `SKILL.md`, then run:

```sh
python3 .claude/skills/verify/scripts/rust-hub-closed.py --output-dir /tmp/oap-rust-hub-closed-<unique-name>
```

The script compiles a standalone consumer of the public Rust Hub library,
reusing only synthetic adapter setup from the integration fixture. Its own
public open/state/list/session/open calls must observe adapter cleanup for
both closed-at-open forms and ID reuse after a closed snapshot. Require exit 0,
`PASS`, and all three observations in `consumer.txt`; a compile failure is not
a regression reproduction. The consumer runs with empty HOME/data/config and
no credentials or providers. Its temporary HOME is removed after execution.
This proves the Rust library surface; it makes no claim about a Rust CLI.
