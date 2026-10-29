## Copilot context ceiling without pricing tiers

Run the shared macOS baseline from `SKILL.md`, then:

```sh
python3 .claude/skills/verify/scripts/copilot-context-backend.py --output-dir /tmp/oap-copilot-backend-<unique-name>
```

The loopback HTTP server returns Copilot-shaped `data[].capabilities.limits`
without billing tiers. The real TUI refreshes with `GET /models`, retains the
200K Claude / 272K GPT default prompt windows, accepts their respective 1M /
1.05M total-context ceilings, refuses ceiling plus one without changing config,
and restores each default. A smaller 200K model checks that the fix does not
apply one model's ceiling to another. Require exit 0 and `PASS`; inspect
`requests.json`, `results.json`, `mock-models.json` and the terminal frames.

To reproduce the shipped alpha.28 refusal instead, pass both
`--binary /absolute/path/to/alpha28/oapx --expect-alpha28-refusal`. This asserts
that the shipped TUI refuses the larger total-context limits at each default
prompt window. The ordinary invocation is the regression test and must fail
against alpha.28.

All metadata and credentials are synthetic. The child uses a temporary HOME;
macOS isolation denies real HOME reads and Security services and allows only
loopback network traffic. The HTTP server and child are stopped on failure and
success. No live provider acceptance, billing or account entitlement is tested.

## Versioned Opus 5.5 catalog

```sh
python3 .claude/skills/verify/scripts/copilot-context-backend.py --opus55-versioned --output-dir /tmp/oap-copilot-opus55-<unique-name>
```

The checked-in fixture uses the official CLI/SDK limits shape. Its legacy total
327680 is the human-reported number; its current 1M/128K limits match models.dev
and the primary upstream API-version A/B report. It is synthetic, not a capture
of the human's account. The mock responds with legacy metadata unless the request
sends `X-GitHub-Api-Version: 2026-08-01`.

Start from a cached legacy Opus row: 1M is refused at 327680. Then the real
`/model refresh` must negotiate the versioned view, accept 1M, refuse 1000001,
and restore the metadata default. Inspect `requests.json` for the API version
and the before/after terminal frames. alpha.29 fails this regression after the
refresh because it sends no API-version header. Existing per-model cases still
run, and `test-unit-providers` checks the same version on the Copilot inference
wire and its absence for another provider.

`test-unit-utils` also verifies the model-policy enablement request uses the
same API version, so login policy calls do not retain an unversioned gateway path.
