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
