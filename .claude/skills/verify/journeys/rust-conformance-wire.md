# Rust conformance framing and transcript

Use the toolchains and isolated execution described in `SKILL.md`, then run
from the repository root

```sh
python3 .claude/skills/verify/scripts/rust-conformance-wire.py --output-dir /tmp/oap-conformance-wire-<unique-name>
```

The script builds the Rust endpoint and a standalone public `oap-conformance`
consumer. The consumer sends initialize and capabilities frames to the real
endpoint, parses its answers, and assembles the observed transcript. It also
checks a synthetic early-event admission, duplicate removal, probe exclusion,
exact maximum cursors and malformed JSON rejection. Require exit 0 and PASS
in `consumer.txt`. Builds and the consumer source are retained.

Runtime HOME/data/config are temporary and removed. The child is killed and
reaped on every exit. The synthetic memory adapter needs no credentials.
Run the complete baseline too. This journey covers the framing/transcript
slice, with process-client deadlines and the full check runner covered by
later slices.
