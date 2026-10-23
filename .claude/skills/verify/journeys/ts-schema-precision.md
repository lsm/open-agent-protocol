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
