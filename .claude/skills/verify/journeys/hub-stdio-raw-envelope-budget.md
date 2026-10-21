## Hub stdio raw envelope budget journey

After changing hub stdio decoding or envelope gating, rebuild `oapx`, then run
this real-process differential without Go's test cache:

```sh
OAP_OAPX_BIN="$PWD/zig/zig-out/bin/oapx" go test ./go/cmd/goap -run '^TestHubStdioBudgetsTheRawRequestMember$' -count=1 -v
zig build --build-file zig/build.zig test-unit-hub -Doptimize=ReleaseSafe
```

The test sends each real hub compact, whitespace-padded and Unicode-escaped
open envelopes at 16 MiB−1, 16 MiB and 16 MiB+1. The first two must open the
memory session; the last must return `request_too_large` even when the raw
value compacts below the cap. The oversized padded value must also be refused
by submit, resolve, cancel and settings. Wrapper bytes, an escaped `request` key and
surrounding whitespace must not change the measured value span. Retain the
verbose output. The existing hub differential CI lane includes this test and
builds ReleaseSafe: processing the real 16 MiB boundaries in Debug can exhaust
the CI job timeout.
