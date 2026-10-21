## Rust validator consumer journey

The Rust validator is a library. Its `validate` example calls the public
`Validator` API on trace files and prints the actual diagnostics as JSON;
exit 0 means valid, exit 1 means invalid. Replay the two shared refresh-window
fixtures, mutate each response's revision, and delay each response until after
a refresh that withdraws the feature to prove rejection is semantic:

```sh
python3 .claude/skills/verify/scripts/rust-refresh-window.py --output-dir /tmp/oap-rust-refresh-<unique-name>
```

The positive traces must report empty diagnostics and exit 0. Their mutated
responses must report `stale_capability_revision`, expected `v1`, actual `v2`,
and exit 1, with no decode or schema failures. A response delayed until after
refresh must instead report `unavailable_capability` under the refreshed
features while retaining the admitted revision echo, matching Go and Zig.
The script retains each input and result.
The baseline script runs this journey too, and validates the positive traces
with the rebuilt `oapx` binary.

Replay every steer fixture through both CLIs, the Rust `validate` example and
`goap validate --format json`:

```sh
python3 .claude/skills/verify/scripts/rust-steer-parity.py --output-dir /tmp/oap-rust-steer-<unique-name>
```

Every steer fixture not listed as a Rust known gap must draw the same code set
from both CLIs as the manifest declares, and exit 0 exactly when it draws none.
The script prints how many agreed and how many it skipped as listed gaps.
