# Go SDK usage consumer journey

After changing Go SDK usage types or decoding, rebuild `oapx` in ReleaseSafe and run:

```sh
python3 .claude/skills/verify/scripts/go-sdk-usage.py --output-dir /tmp/oap-go-sdk-usage-<unique-name>
```

The standalone consumer imports the public `go/sdk` package and calls provider
and agent unary and streaming APIs against a credential-free fixture process.
Absent counts stay absent, independent measured zeros stay zero, and all six
counts retain `2^53+1` and `uint64` maximum exactly. Present null, negative,
malformed and out-of-range counts must return typed `malformed_response`.
The script retains source, inputs and actual JSON results, and replays usage
boundaries through all three validator CLIs with zero disagreements.
No provider, credentials or GUI is used. Inspect the output and run the full
baseline too; unit checks alone do not prove the consumer result.
