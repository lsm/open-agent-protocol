## Shared Copilot CLI corpus

Use the shared build/tool setup in `SKILL.md`, then run from the root:

```sh
python3 .claude/skills/verify/scripts/copilot-harness-corpus.py --output-dir /tmp/oap-copilot-corpus-<unique-name>
```

The first baseline is `completed-text`: Zig's production native decoder and
reducer replay the authored synthetic observations against fixed expected OAP.
The script wraps that matching event sequence with its admission exchange and
runs the public Go and oapx validators. Require exit 0 and `PASS`; inspect
`zig-replay.txt`, the full trace and both validator outputs. The replay also
refuses a mislabeled outbound response, so its direction is part of the contract.

The pin is CLI 1.0.98 / protocol 3; source and artifact provenance come from the
existing ledger. This is synthetic schema/ledger-based data, not a new live
recording. It starts no Copilot process, uses no token, and performs no model
call. Go currently exposes pin metadata only: a successful catalog or corpus
check does not claim the Go native adapter is implemented. Later slices expand
this corpus before Go and Rust consume it; unsupported host capabilities remain
unadvertised. The Rust lane follows separately.
