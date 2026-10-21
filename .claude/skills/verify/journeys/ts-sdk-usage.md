# TypeScript SDK usage consumer journey

After changing TypeScript SDK usage decoding, install dependencies with
`npm ci --ignore-scripts`, rebuild `oapx` in ReleaseSafe, then run:

```sh
python3 .claude/skills/verify/scripts/ts-sdk-usage.py --output-dir /tmp/oap-ts-sdk-usage-<unique-name>
```

The standalone public consumer calls provider/agent unary and streaming APIs
over a credential-free fixture process. Unknown counts remain absent and
independent zeros remain numeric zero. All six counters retain `2^53-1` as a
number; `2^53` and `uint64` maximum complete normally with that counter absent
and an exact decimal `unrepresentable_count` signal on the usage object.
Malformed/null, negative, fractional and out-of-uint64 counts return typed
`malformed_response`. Integral decimal/exponent spellings remain refused
pending #1398; the validator comparison excludes those known divergences and
uint64 overflow while requiring zero disagreements on the agreed boundaries.

Inspect retained input/output JSON, including safe, unsafe and mixed counts.
Run the complete baseline too. No provider call, credentials or GUI is used.
