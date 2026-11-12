# Harness feature matrix

The matrix in `go/tools/featurematrix` renders one table cell per current Go
harness pin and behavior column, and every cell must come from a recorded test
result, never from documentation or a claim.

The static binding lives in `go/tools/featurematrix/matrix.json`: rows name the
harness, its pin, capability revision and corpus; cells name the package, the
full Go test or corpus subtest id, whether the assertion is a positive
capability or an explicit refusal, and the fixture path when the evidence is a
corpus case. The tool refuses to publish when a binding names a pin, revision
or corpus that differs from `harnesses/<id>.json`'s current version: after a
pin move, re-bind the evidence before the matrix can generate again.

To reproduce what CI publishes:

```sh
mkdir -p /tmp/feature-matrix
go test -json -count=1 ./go/adapter/... > /tmp/feature-matrix/adapter-test.json
go run ./go/tools/featurematrix \
  -test-output /tmp/feature-matrix/adapter-test.json \
  -out /tmp/feature-matrix/out \
  -sha "$(git rev-parse HEAD)"
```

Check all of these before saying the matrix works:

- `out/harness-feature-matrix.md` exists and every cell shows `pass` or
  `unsupported`; `unsupported` appears only where a refusal test passed.
- `out/harness-feature-matrix.json` records, for each cell, the harness pin,
  capability revision, test id, fixture path and the actual test status.
- `out/matrix-test-evidence.jsonl` holds the filtered `go test -json` events
  for the bound tests only.
- A skipped, missing, renamed or failing bound test makes the tool exit
  non-zero and write `out/generation-failed.md` instead of the matrix, and the
  CI job fails with the artifact still uploaded.

Mutation-check the failure paths exactly as the positive one: rename a bound
test in `matrix.json` (generation must fail naming it), and skip a bound test
with `-run` filtering (generation must refuse). Restore both afterwards.
