# Rust SDK usage consumer journey

After changing Rust SDK token types or usage mapping, run:

```sh
python3 .claude/skills/verify/scripts/rust-sdk-usage.py --output-dir /tmp/oap-rust-sdk-usage-<unique-name>
```

Build `oapx` in ReleaseSafe first. The script builds a standalone consumer importing the public `oxap-sdk` crate
and connects it to the credential-free OAP fixture process. Provider and agent
unary and streaming results must keep absent usage unknown, preserve empty and
partial usage, and retain independent zeros and all six `uint64` maxima exactly.
Present null values, negative, malformed and out-of-range counts must produce typed
`malformed_response`, not a successful completion with missing usage.
Consumer source, inputs, actual JSON results and stderr are retained. No live
provider, credentials or GUI is used. Run the full baseline too, and inspect
the retained public-consumer output before claiming success.

The consumer explicitly allows a 10-second fixture handshake. The first fixture
executes after a two-second synthetic startup delay. `delayed-startup.json`
retains the configured delay, per-handshake budget and complete consumer elapsed
time. This proves usage observation tolerates bounded
startup delay. SDK transport tests still exercise their explicit timeout cases.
The wrapper uses exec, retaining one owned child PID across startup and fixture
execution. A timeout or failed usage assertion must fail the journey.

The journey also replays usage boundaries through the Go, Rust and Zig validator
CLIs: empty, zero and maximum usage pass; negative, malformed and non-object
usage, including present null counters or usage objects, fail with `schema_invalid`. The disagreement count must be zero.
