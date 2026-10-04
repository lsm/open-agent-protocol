# OpenCode v1.18.34 mapping ledger

Status: pin move from v1.18.32. This ledger records only what differs from
[`opencode-v1.18.32-mapping.md`](opencode-v1.18.32-mapping.md), which with
[`opencode-v1.18.29-mapping.md`](opencode-v1.18.29-mapping.md), the full
mapping, stays normative for everything not restated here.

## Provenance

- Repository: `https://github.com/anomalyco/opencode`
- Release: `v1.18.34`, latest stable on 2026-10-03, published 2026-09-30
- Commit: `aec0b9a6d8898f68f923aaf08b7306d931fd9d76`
- Commit tree: `b70e30aeb84feb7a3affb06c94d23709675016f8`

```sh
git clone https://github.com/anomalyco/opencode.git
git -C opencode checkout aec0b9a6d8898f68f923aaf08b7306d931fd9d76
git -C opencode rev-parse HEAD 'HEAD^{tree}'  # aec0b9a..., b70e30a...
git -C opencode describe --tags               # v1.18.34
```

Release artifacts, fetched with `gh release download v1.18.34 -R
anomalyco/opencode`; the archive digests match the release's own asset
digests, and the darwin-arm64 binary reports `1.18.34`:

| Platform | Kind | Name | SHA-256 | Bytes |
| --- | --- | --- | --- | --- |
| linux-x64 | archive | `opencode-linux-x64.tar.gz` | `0f22479647226d1d2dd99595d20082ee7bda3870b62dc6a90b41efc1a71d7e9a` | 60665582 |
| linux-x64 | binary | `opencode` | `9ca0b9953d49997601655e54f846a3efa464f237e47c6f1b04716d0f2e64c4c2` | 185632896 |
| darwin-arm64 | archive | `opencode-darwin-arm64.zip` | `8522b70f545184b3a8d97c5ca4f814093b2476d72aebfda8c48bcd072ec31d1b` | 45538151 |
| darwin-arm64 | binary | `opencode` | `7b63b34fafabded7d9231f6a9032755d0cdeaf8b9d2b70df8e25535471469eea` | 144257280 |

## Wire difference against v1.18.32: none

Source. `git diff --stat v1.18.32 v1.18.34` touches 193 files. Over
`packages/schema/src`, `packages/protocol/src`, `packages/server`,
`packages/core/src/session` and `packages/cli/src/commands/handlers/serve.ts`
it touches two:

- `packages/server/package.json`, the version string only;
- `packages/core/src/session/runner/llm.ts`, which adds
  `x-opencode-session-id` and, for a child session,
  `x-opencode-parent-session-id` to the headers OpenCode sends its model
  provider. That is OpenCode's outbound request, not the served API.

`git ls-tree -r` over the same paths lists the same number of blobs at both
tags (64, 22, 28, 25 and 1), and the six blobs each corpus case cites are
byte-identical to the v1.18.32 ledger's table.

The rest is the web site and docs, console, stats, TUI, provider and MCP
changes under `packages/opencode`, and CI; none of it is read by the adapter.

Live probe (2026-10-03, the darwin-arm64 binary, `opencode serve` on loopback
with an empty `HOME`, no credentials) answered exactly as v1.18.32 did:
`GET /api/health` → `{"healthy":true}`; `POST /api/session` with `{}` → a
`{"data":{…}}` session record; `GET /api/session/<id>/event` on a silent
session → no status line within 3s; `POST /api/session/<id>/wait` → the same
503 `ServiceUnavailableError`; `POST /api/session/<id>/interrupt` on an idle
session → 204; `GET /api/session/active` → `{"data":{}}`; `GET /openapi.json`
→ the web app's HTML.

The gated server test passes 3x against the pinned darwin-arm64 binary:
`OAP_OPENCODE_INTEGRATION=1 OAP_OPENCODE_BIN=<abs> OAP_OPENCODE_SHA256=7b63b34f…
OAP_OPENCODE_TAG=v1.18.34 go test -run TestOpenCodeServerIntegration
./go/adapter/opencode/`.

## Corpus

`fixtures/adapters/opencode-v1.18.34/` carries all fifteen cases forward from
`opencode-v1.18.32/`; none was re-recorded. Every `native.jsonl`,
`mapping.json`, `omissions.json` and `catalog.json` is byte-identical.
`manifest.json` and each `case.json` name the new tag, commit and tree, and
each `expected-oap.json` differs only in `capability_revision`.

## Capability revision

The advertised surface is unchanged; the revision moves only because the
endpoint version does (Decision 0033): `opencode-v1.18.34-oap-v3`, keeping the
`-v3` suffix of `opencode-v1.18.32-oap-v3` whose descriptor content it repeats.

## Adapter changes

None in either tree. The Go goldens in
`go/adapter/opencode/testdata/port-{goldens,scenarios}.json` were re-recorded
with `OAP_UPDATE_OPENCODE_PORT_GOLDENS=1` and differ only in the version and
revision strings.

v1.18.32 is retired: its corpus directory is removed and its ledger remains.

## Event stream establishment

`GET /api/session/:sessionID/event` on the v1.18.34 darwin-arm64 binary (sha256
`7b63b34fafabded7d9231f6a9032755d0cdeaf8b9d2b70df8e25535471469eea`) sends no
status line or headers until the session's first event: `curl -i` against a
fresh idle session read nothing in 40 seconds. (`after=-1` is refused 400,
"Expected a value greater than or equal to 0", which is why both trees omit a
negative cursor.) The Go client already tolerates this: `Subscribe` waits
`subscribeEstablishGrace` (250 ms) for the response, then returns and lets it
arrive later, failing the subscription if it is not a 200. The Zig port waited
for the headers before answering `session.open`, so `oapx serve agent
--backend opencode` never opened against a real server. It now waits the same
250 ms, then opens, and ends the session if the status that eventually arrives
is not 200. Against the binary, the port opens, and a submitted prompt streams
its events once the held headers arrive and settles `run.completed`; the trace
validates. That run used the server's own default model, since the driver
configured none.
