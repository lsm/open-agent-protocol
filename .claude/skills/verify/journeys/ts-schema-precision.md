# TypeScript transport and shutdown cardinality

From the repository root, install the client prerequisites and run:

```sh
npm --prefix clients/ts ci --ignore-scripts
python3 .claude/skills/verify/scripts/ts-schema-precision.py --output-dir /tmp/oap-ts-schema-precision-<unique>
```

The helper builds the client, packs its public declarations and installs that
archive into a separate temporary consumer. The real TypeScript CLI compiles
consumer imports from `oap-client`, retaining source, diagnostics and exit codes
under the output directory. Temporary installs and compiler children are removed.

Observe successful compilation for absent transports, one or several transports,
`choose` with one or several active runs, and empty or nonempty active runs for
`draining` and `stopping`. Held/lost queues remain optional and may be empty.
Narrowing `outcome` to `choose` exposes a guaranteed first active run, even with
`noUncheckedIndexedAccess` enabled. Observe exactly one TS2322 assignment error
for each of `transports: []` and `{outcome: 'choose', active_runs: []}`.

`clients/ts/test/schema.test.ts` imports the same consumer test so the existing
client test command executes it without an index or package-script change.
Run `npm --prefix clients/ts test` too. These checks prove the public declaration
surface against the unchanged schema, not runtime validation of untyped JSON;
no endpoint, live provider, GUI or port 8399 is used.

## Core auth payload declarations

The same installed consumer also imports the provider listing, start, event,
cancel and completion payload types named by the existing eight auth envelope
constants. Before the declarations exist, the public imports fail with TS2305.
Valid controls compile and execute synthetic JSON trace emitters once the types
are available. The controls cover all optional provider/event fields, omitted
optional fields, all auth status/kind vocabulary, empty provider listings, and
success/failed/cancelled completion shapes. A URL event may also carry a message,
and a progress event may also carry a URL; the schema permits both.

Empty auth-kind lists, URL events without a URL and progress events without a
message must produce one TS2322 assignment error. Those emitting cases use
`noEmitOnError` and exit 1 when refused; the original non-emitting cardinality
cases still exit 2. No account operation or real credential is involved.

The helper builds the public Go/Rust/Zig validator CLIs and feeds them the two
traces actually printed by the installed consumer. For each of three invalid
variants (empty provider auth kinds, missing URL, missing progress message),
require exactly `schema` / `schema_invalid` at index 1, 4 or 5 respectively in
all three trees. A repaired copy of each must exit 0 without diagnostics.
Outputs and the scoped comparison are retained in `validator-results.json`.
This establishes those auth cases only, not full validator parity.

With no other edits or builds running in this worktree, run:

```sh
python3 .claude/skills/verify/scripts/ts-schema-precision.py --mutation-check --output-dir /tmp/oap-ts-schema-mutations-<unique>
```

It weakens the owned TypeScript nonempty tuple, URL requirement and progress
message requirement one at a time. Each targeted invalid consumer must compile
and execute against the packed public client, emit the bad trace, and be
rejected by all three unchanged validators with the exact diagnostic above.
An unrelated build/compiler error does not count as a killed mutation. The
source is restored in `finally`; after successful probes the helper rebuilds,
reruns the consumers and replays all controls again. If interrupted, inspect
source restoration before continuing and rebuild before using compiled output.
Run the complete verify baseline and the full client suite/integration tests
without `OAP_TS_SKIP_INTEGRATION`; its PTY journey uses isolated, denied-network
fixture state. No real provider, account or credential is exercised.

## Session model-switch and provider-attach declarations

The installed consumer also imports `SessionModelSwitchRequest`,
`SessionModelSwitchResponse`, `ProviderAttachment`,
`SessionProviderAttachRequest` and `SessionProviderAttachResponse`. Before these
exports exist, the valid public imports fail with TS2724/TS2305. Four controls
compile and execute model-switch/provider-attach traces derived from the
unchanged shared valid fixtures. They include and omit degraded-feature opt-in,
previous model id and provider service id independently of required fields.
These checks add no model-capture, catalog or admission behavior.

Each missing request/response model id, attachment upstream provider id and
attach-response provider id produces exactly one TS2741 error and no emitted
JavaScript. The helper also removes those fields from the emitted valid wire:
all three public validators must return exactly `schema` / `schema_invalid`
at index 6 (requests) or 7 (responses). Restoring the field must return exit 0
without diagnostics. This is scoped declaration/wire evidence, not full
validator parity or an actual account/provider operation.

`--mutation-check` additionally makes each of the four required declaration
fields optional in turn. Each negative installed consumer must then compile,
execute and emit the corresponding schema-invalid trace. Require the same exact
validator refusal, then restore the source and rerun all controls. Full client
and integration tests and the complete verify baseline with isolated PTY remain
required. No shared setup/index or other owner's helper is changed.

## Exact uint64 wire values

Run the installed public-consumer and shared-wire journey separately:

```sh
python3 .claude/skills/verify/scripts/ts-schema-precision.py --uint64-check --output-dir /tmp/oap-ts-uint64-<unique>
```

The packed client must expose every uint64 field as `bigint`, including zero
and one, and `serializeEnvelope` must emit exact JSON number tokens. Observe
216 usage cases across all six counts and six locations, plus payload positions,
history tokens and integral sequence spellings. Counts at `2^53+1` and uint64
maximum must round-trip unchanged. Number-valued counter/cursor consumers must
fail compilation; bigint sums must work for both small and large values.
Ordinary JSON/tool-result numbers, strings resembling numeric markers, absent
usage and omitted counts retain their existing behavior. Invalid usage counts
are refused before projection, including raw `-0`, decimals, exponents and null.
Non-object usage containers, including numeric tokens, are refused at all six sites.

The same separately installed consumer runs a real credential-free loopback
HTTP server on an ephemeral port, closes all connections in `finally`, and proves
outgoing serialization, incoming SSE, exact resume query/header values above
`2^53`, adjacent large frame-ID mismatches, overflow and replay-gap properties.
It uses no provider/account operation, port 8399 or external service.

The consumer reads the shared usage fixtures as raw one-envelope-per-line
arrays, rather than decoding their counts through `JSON.parse`. Every valid
case is emitted through the public parser/serializer and replayed through the
three public validators. Refused cases retain their original bytes, including
negative zero, for exact phase/code comparison with the unchanged manifest.
`consumer/corpus/results.json` and `uint64-validator-results.json` retain
acceptance, errors, emitted traces and validator results. This is scoped usage
proof; it does not claim full validator parity. Run the complete repository
baseline, isolated PTY and full clients/ts suite with integration enabled too.
