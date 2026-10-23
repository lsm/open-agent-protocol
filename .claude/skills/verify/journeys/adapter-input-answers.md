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
failure. There is a separate pre-existing semantic validator gap: all three
validators reject optional empty text as `unmatched_interaction`, contrary to
Decision 0056. The script explicitly checks this known result and the absence
of schema errors; it does not call it a conforming validator result. Other
valid traces must pass. Validator sources and shared fixture metadata are
outside this helper/serializer change and remain untouched.

There is also a pre-existing Zig adapter-helper divergence, separate from the
semantic validators: `zig/src/adapter/contract.zig`'s `validInputAnswer` rejects
empty text unconditionally, and its `Question` type has no `required` field.
Thus Zig adapter consumers do not yet share the corrected Rust/Go helper
contract. This was reproduced through the public Zig helper with an optional
text question and `text = ""`; a normative acceptance assertion fails at
runtime. Zig adapter changes are outside the bound file set and need their
own follow-up. This journey makes no claim of Zig adapter-helper parity.
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
