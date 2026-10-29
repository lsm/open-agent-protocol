# Versioned Copilot Opus 5.5 context

Use the shared macOS build and credential isolation setup from `SKILL.md`.
Run from the repository root after building ReleaseSafe.

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
