# OpenCode v1.18.32 mapping ledger

Status: pin move from v1.18.29. This ledger records only what differs from
[`opencode-v1.18.29-mapping.md`](opencode-v1.18.29-mapping.md), which remains
the full mapping and is still normative for everything not restated here.

## Provenance

- Repository: `https://github.com/anomalyco/opencode`
- Release: `v1.18.32`
- Commit: `545f51d26cc39a907d2867492d498d9607ea5fa4`
- Commit tree: `b443aa2f11f402603d7911040fc9067ad65cf475`

```sh
git clone https://github.com/anomalyco/opencode.git
git -C opencode checkout 545f51d26cc39a907d2867492d498d9607ea5fa4
git -C opencode rev-parse HEAD 'HEAD^{tree}'  # 545f51d..., b443aa2...
git -C opencode describe --tags               # v1.18.32
```

Release artifacts, fetched with `gh release download v1.18.32 -R
anomalyco/opencode`; the archive digests match the release's own asset
digests:

| Platform | Kind | Name | SHA-256 | Bytes |
| --- | --- | --- | --- | --- |
| linux-x64 | archive | `opencode-linux-x64.tar.gz` | `3046e0404fdc60fb80307e7a47824ba07477364178a4d09baa8548496dd6d43b` | 60608353 |
| linux-x64 | binary | `opencode` | `513f500a1a5ea1dc7d865547ac87b32a8936334e8d5abd5b3ff585c45a170080` | 185165952 |
| darwin-arm64 | archive | `opencode-darwin-arm64.zip` | `fa643f93401c13508d8d513780e54ce9cc01203d501114be9b88d62408b8101f` | 46299070 |
| darwin-arm64 | binary | `opencode` | `a3c45d4e1d6620b436851f1ef6b25c71befcf06a382e279a1eb1c2196424395e` | 144602594 |

## Wire difference against v1.18.29: none

Source. `git diff --stat v1.18.29 v1.18.32` touches 160 files under
`packages/`. Of the inspected sources the v1.18.29 ledger names, only
`packages/server/package.json` changed, and only its version string.
`git ls-tree -r` over `packages/schema/src`, `packages/protocol/src`,
`packages/server/src`, `packages/core/src/session` and
`packages/cli/src/commands/handlers/serve.ts` lists the same 140 blobs at both
tags. The six blobs each corpus case cites are byte-identical:

| Member | Path | Blob |
| --- | --- | --- |
| `session_event_blob` | `packages/schema/src/session-event.ts` | `3a559c3e38a401218ac36e3f79051172df4dbe3d` |
| `session_input_blob` | `packages/schema/src/session-input.ts` | `40babac105f66671baeb59679e275f6536a5ae26` |
| `session_delivery_blob` | `packages/schema/src/session-delivery.ts` | `9b678dabf9f910b173f2b2cfddebacbd11922264` |
| `session_group_blob` | `packages/protocol/src/groups/session.ts` | `8ce85ef79686dd5f448c9b31a9416c22608e7665` |
| `server_handler_blob` | `packages/server/src/handlers/session.ts` | `5b7d354b04fc32567e41582e6a8e74537be6e57d` |
| `core_session_blob` | `packages/core/src/session.ts` | `2dabfb2d6fba2eeff6306abcae0f5fb8c99b6f13` |

The HTTP API groups are generated from `packages/protocol`, so the route set,
the SSE event inventory and the OpenAPI surface are unchanged with them.

Changes outside that boundary, none of which the adapter reads:

- `packages/opencode/src/server/routes/instance/httpapi/middleware/error.ts`
  maps `ConfigErrorV1.RemoteAuthError` to a 400 JSON body. This is the legacy
  instance API, not the `/api/session` routes, and only on a remote-config
  auth failure.
- `packages/opencode/src/session/message-v2.ts` narrows which Bedrock models
  receive image attachments; provider-side, not wire.
- ACP service changes (`packages/opencode/src/acp`); the ACP adapter is a
  different boundary with its own pin.
- Provider SDK dependency bumps, console, web, stats, TUI and desktop.

Live probe (2026-09-24, darwin-arm64 binaries of both tags, `opencode serve`
on loopback with an empty `HOME`, no credentials). Both tags answered
identically:

- `GET /api/health` → `{"healthy":true}`
- `POST /api/session` with `{}` → `{"data":{…}}` session record
- `GET /api/session/<id>/event` on a silent session → no status line within 3s,
  the deferred-header behaviour the v1.18.29 live-gate finding records
- `POST /api/session/<id>/wait` → 503
  `{"_tag":"ServiceUnavailableError","message":"Session wait is not available
  yet","service":"session.wait"}`
- `POST /api/session/<id>/interrupt` on an idle session → 204
- `GET /api/session/active` → `{"data":{}}`

`GET /openapi.json` and `GET /api/openapi.json` on the served process return
the web app's HTML at both tags, not the spec, so the OpenAPI comparison rests
on the source tree above.

The gated server test passes against the pinned darwin-arm64 binary:
`OAP_OPENCODE_INTEGRATION=1 OAP_OPENCODE_BIN=<abs> OAP_OPENCODE_SHA256=a3c45d4e…
OAP_OPENCODE_TAG=v1.18.32 go test -run TestOpenCodeServerIntegration
./go/adapter/opencode/`.

## Corpus

`fixtures/adapters/opencode-v1.18.32/` carries every one of the fifteen cases
forward from `opencode-v1.18.29/`. No case was re-recorded:

- Each `native.jsonl` and `mapping.json` and `omissions.json` is byte-identical
  to v1.18.29. The wire did not change for any case, by the blob identity
  above, and none of these files names a version.
- `manifest.json` and each `case.json` name the new tag, commit and commit tree.
  The cited blobs are unchanged because the files are.
- Each `expected-oap.json` differs only in `endpoint.version` and
  `capability_revision`, which the adapter derives from the catalog.

The v1.18.29 native lines were not live recordings either; they are the
normalized frames that ledger's corpus plan describes, and they stay so.
A provider-backed live recording still needs a model credential, which no gate
passes to a child.

## Capability revision

The advertised surface is unchanged. The revision moves only because the
endpoint version does (Decision 0033): `opencode-v1.18.32-oap-v2`. The `-v2`
suffix is kept to say the descriptor content is the same as
`opencode-v1.18.29-oap-v2`. Since the endpoint journals every served session,
`oapx serve agent --backend opencode` serves this same descriptor under this
same revision, so the version carries no `oapx_capability_revision`.

## Adapter changes

None in either tree. Both read the tag, commit, tree and revisions from
`harnesses/opencode.json`. The Go goldens in
`go/adapter/opencode/testdata/port-{goldens,scenarios}.json` were re-recorded
with `OAP_UPDATE_OPENCODE_PORT_GOLDENS=1`; they differ only in the version and
revision strings.

v1.18.29 is retired: its corpus directory is removed and this ledger's
predecessor remains.
