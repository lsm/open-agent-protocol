# Decision 0056 input-answer helpers and emitted wire

Use synthetic questions/answers and the unchanged shared input trace. No native
harness, provider, credentials or service is involved. Run from the repository
root:

```sh
python3 .claude/skills/verify/scripts/adapter-input-answers.py --output-dir /tmp/oap-input-answers-<unique>
```

The public Rust example and Go reference call the real adapter helpers and
serialize typed protocol answers. Decision 0056 is the oracle: optional text may
be empty or omitted, every required question must be answered, and required
text must be nonempty. Choice, duplicate and foreign-answer refusals remain.
Unicode text is preserved. Both helpers must match the normative expectations,
not merely each other.

The script places emitted answers in draft answers, a resolve request and a
resolved event in a complete shared-fixture-shaped exchange. Rust, Go and Zig
must agree on the recorded diagnostics. An empty text answer must emit `text: ""`;
a choice answer must not gain a text member. Removing text from the valid empty
answer must fail schema validation, and restoring it must remove that schema
failure. Restored optional empty text and all other valid emitted traces must
exit 0 in Rust, Go and Zig with no schema or semantic refusal. The former
optional-empty semantic validator gap is closed under Decision 0056.

The Zig adapter helper now carries each question's `required` flag. Optional
text accepts an explicit empty text field; required text and malformed choices
remain refused. Run the public helper and actual adapter call-site contracts:

```sh
zig test --dep oap_types -Mroot=zig/src/adapter/contract.zig -Moap_types=zig/src/protocol/oap/types.zig
zig build --build-file zig/build.zig test-unit-adapter -Doptimize=ReleaseSafe -j4
```

The oapx endpoint test submits optional empty text beside required text, checks
that the native tool receives the answer, preserves `text: ""` in the resolved
event, and validates the complete emitted exchange. Pi's required text dialog
must still reject empty text without writing a native response. No
`known_gap` exemption applies: every valid emitted or restored-empty trace
must exit 0 in Rust, Go and Zig; the missing-text mutation must exit 1.
Empty answer sets are tested at the helper surface only: the wire schema still
requires at least one answer in a submitted resolution. Mixed answers remain
invalid rather than being silently repaired by serialization.

Protocol tests also exercise the nested typed draft/request/event payloads.
The existing string-based answer representation cannot retain whether an empty
text field was absent on input; serialization treats no selections as text.
This does not relax schema validation of received envelopes.

With no concurrent Rust/Go builds, verify the six independent source changes:

```sh
python3 .claude/skills/verify/scripts/adapter-input-answers.py --mutation-check --output-dir /tmp/oap-input-answers-mutations-<unique>
```

Each helper/serializer regression must fail runtime assertions. Compilation
failure is inconclusive. The script restores source in `finally` and reruns
the focused suites. Logs, JSON inputs, emitted traces and validator outputs are
retained in the output directory. Also run the repository baseline, full Rust
all-feature tests, fmt, clippy with warnings denied, Go checks and all three
comment guards. Inspect the baseline's isolated PTY reply/quit evidence. Live
providers, real IME input and native harnesses remain unverified.
