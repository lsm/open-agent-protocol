## Python SDK usage journey

After changing usage projection or public `Usage` fields, rebuild the Zig binary
and run this standalone public Python consumer from the repository root:

```sh
zig build --build-file zig/build.zig -Doptimize=ReleaseSafe
python3 .claude/skills/verify/scripts/python-sdk-usage.py --output-dir /tmp/oap-python-usage-<unique-name>
```

The script uses `python3.11` if available; pass `--python-sdk` to select another
Python >=3.11 interpreter. It launches a fixture stdio endpoint and observes
`provider.complete`, `provider.stream`, `agent.run` and `agent.stream` through
the public API, saving each input and JSON consumer output.

All six counters must independently preserve absent, measured zero and uint64
maximum values. Missing usage stays `None`; an empty object has all counters
unknown. A supplied total must survive partial counts without recomputation.
Null usage objects or counters, negatives, fractions, floating-point numbers,
booleans, strings, objects, arrays and overflow counts must raise
`MakaiProtocolError` with code `malformed_response` on every API.

The same script feeds boundary traces to the real Go, Rust and Zig validators.
Their exit statuses and diagnostic codes must agree: valid measurements pass,
malformed usage fails with `schema_invalid`, and `validators.json` records zero
disagreements on its focused subset. `usage-count-validation.md` covers the full
Decision 0065 raw-token matrix, including uint64 overflow and noncanonical
spellings, across all six shared-usage sites. `python-sdk-negative-zero.md` proves the SDK raw `-0` refusal on all four
public paths.
This proves the SDK wire boundary, not a live provider's accounting.
