# Rust SDK usage consumer journey

After changing Rust SDK token types or usage mapping, run:

```sh
python3 .claude/skills/verify/scripts/rust-sdk-usage.py --output-dir /tmp/oap-rust-sdk-usage-<unique-name>
```

Build `oapx` in ReleaseSafe first. The script builds a standalone consumer importing the public `oap-sdk` crate
and connects it to the credential-free OAP fixture process. Provider and agent
unary and streaming results must keep absent usage unknown, preserve empty and
partial usage, and retain independent zeros and all six `uint64` maxima exactly.
Negative, malformed and out-of-range counts must produce typed
`malformed_response`, not a successful completion with missing usage.
Consumer source, inputs, actual JSON results and stderr are retained. No live
provider, credentials or GUI is used. Run the full baseline too, and inspect
the retained public-consumer output before claiming success.

The journey also replays usage boundaries through the Go, Rust and Zig validator
CLIs: empty, zero and maximum usage pass; negative, malformed and non-object
usage fail with `schema_invalid`. The disagreement count must be zero.
