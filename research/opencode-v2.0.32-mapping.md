# OpenCode v2.0.32 mapping ledger

Status: the current pin, a move over
[`opencode-v2.0.26-mapping.md`](opencode-v2.0.26-mapping.md), itself a move
over [`opencode-v2.0.24-mapping.md`](opencode-v2.0.24-mapping.md), which
remains the mapping. This one records only what moved between v2.0.26 and
v2.0.32 and the evidence for it. Everything not named here, including the
v2.0.26 ledger's sections on unprompted inbox items, usage limits and tools
that fail before they are called, is unchanged.

## Provenance

- Repository: `https://github.com/anomalyco/opencode`
- Tag: `v2.0.32`, a git tag with no GitHub release; the latest v2 build on
  2026-10-18 (the `2.0.33` artifact path answers `404`)
- Commit: `843a6c514e606b436a7fe713422ace0a93ad3d61`, committed
  2026-10-16T22:16:52Z, 344 commits after v2.0.26
- Commit tree: `57396484a8423c80eb4ecb0df9f3886d6fc810c1`

As at v2.0.24 and v2.0.26, the artifacts come from
`https://opencode.ai/files/bin/2.0.32/<name>`. That path publishes no checksum
file (`SHA256SUMS`, `checksums.txt` and `<name>.sha256` all answer `404`), so
the digests below are of the files as downloaded. The darwin-arm64 binary is
signed by team `5NZ4Q7NXJ4` and reports `opencode v2.0.32`:

| Platform | Kind | Name | SHA-256 | Bytes |
| --- | --- | --- | --- | --- |
| linux-x64 | archive | `opencode-linux-x64.tar.gz` | `ba3f5d9dd983f12f06981b5b9eb966c03db63266afab28adc4000bbad6adc33c` | 98601332 |
| linux-x64 | binary | `opencode` | `489b51816a77809e841310e80c88e834c28c19ad1639e12ba8a19f08b47ab607` | 211842528 |
| darwin-arm64 | archive | `opencode-darwin-arm64.zip` | `e431e369025ba9554445d0ea8ad65457fbca59aa385ca790724ec6ab488d271b` | 85487805 |
| darwin-arm64 | binary | `opencode` | `e43ec2df6f36842fd17d1a71a866acb9a84884b489988376c19bf9a7250dabbf` | 186957968 |

The catalog admits `2.0.32` on that evidence.

## Source difference, v2.0.26 to v2.0.32

Four of the six blobs the corpus pins are byte-identical at both tags:
`packages/schema/src/session-event.ts` (`0cffae27…`),
`packages/schema/src/session-inbox.ts` (`361c0c49…`),
`packages/core/src/session/execution.ts` (`eeed241a…`) and
`packages/core/src/session.ts` (`39f5f36d…`). So are
`packages/core/src/bus.ts`, `packages/server/src/server-info.ts`,
`packages/core/src/session/projector.ts`,
`packages/schema/src/session-error.ts` and
`packages/core/src/session/to-session-error.ts`. Because `session-event.ts` is
unchanged, every event type and payload member the adapters decode strictly is
unchanged.

The two pinned blobs that changed carry no wire change:

| Path | v2.0.26 | v2.0.32 | Change |
| --- | --- | --- | --- |
| `packages/protocol/src/groups/session.ts` | `d5bf2af1…` | `a9ee5069…` | the Effect upgrade's import paths (`effect/unstable/httpapi` to `effect/http-api`, `Encoding` to `Base64Url`) and a schema annotation on the sessions cursor; the cursor is still base64url of the same input |
| `packages/server/src/handlers/session.ts` | `73a07633…` | `dd85c2f4…` | the same import path move, and nothing else |

Most of the other changes across `packages/schema` are the same upgrade:
`isStartsWith` becomes `isStartingWith`, and IDs gain an `identifier`
annotation. Neither changes what is on the wire. What does change:

| Change | Where | Classification |
| --- | --- | --- |
| A session's idle message gains an optional `error` | `packages/schema/src/session-message.ts` | admitted without change: both trees decode message records leniently and read only `type` (and, for replay, the members they already read) |
| A step that ends on the output limit now continues the run. It publishes `session.synthetic` with a continuation prompt and starts a new assistant message, and after three length-finished assistant messages in a row it fails the step with error type `output-limit` | `packages/core/src/session/runner/{step,llm}.ts` | no change: `session.synthetic` was already a known event both trees pass over, the run still settles at `session.execution.*`, and a step failure's error `type` is an unconstrained string in both trees |
| A tool whose input is malformed or cut off now fails with a longer message holding an input excerpt; `tool.input-incomplete` names the output limit when that was the cause | `packages/core/src/session/runner/publish-llm-event.ts` | no change: the adapters carry the message through, and `TestOpenCodeServerFailsAMalformedToolCallAndTheRunGoesOn` still passes |
| A provider gains optional `encoding` and `compatibility`; model compatibility gains `cacheFormat` and `temperature` | `packages/schema/src/{provider,model,model-compatibility}.ts` | unused: the adapters write no provider or model compatibility |
| A project's `time` gains optional `archived` | `packages/schema/src/project.ts` | unused: the adapters do not decode projects |
| The service config's `env` is merged into the process environment, and a `claude` flag gates `.claude` skill roots | `packages/cli/src/server-process.ts`, `packages/server/src/routes.ts` | unused: the adapters write no service config |
| `session.share` leaves the TUI event list | `packages/schema/src/tui-event.ts` | unused |

The adapter code does not change in either tree. The capability descriptor is
unchanged, so only the revision's version prefix moves:
`opencode-v2.0.32-oap-v5`.

## Corpus

`fixtures/adapters/opencode-v2.0.32` is the v2.0.26 corpus moved forward
(`corpus_from: v2.0.24`, where it was recorded). No case was re-recorded:
every `native.jsonl` is byte-identical. Only the provenance moved (the
manifest's and each case's `tag`, `commit` and `commit_tree`, and the two
changed blobs above in `session_group_blob` and `server_handler_blob`), as did
the expectations' `capability_revision` and the revision and endpoint version
in `go/adapter/opencode/testdata/port-{goldens,scenarios}.json`.

## Process gate

Live, against the pinned darwin-arm64 binary (sha256 `e43ec2df…`), with
`OAP_OPENCODE_INTEGRATION=1` and `OAP_OPENCODE_SHA256` set, a temporary
`HOME`, no credentials, and `sandbox-exec` denying the keychain daemons and
every non-loopback connection: all eight gates in `server_integration_test.go`,
`permission_integration_test.go` and `reopen_integration_test.go` pass three
times. Among them, `TestOpenCodeServerRunsATurnToCompletionAgainstALocalProvider`
completes a real turn against an in-process OpenAI-compatible fake, and
`TestOpenCodeServerReopensItsBoundSessionAfterARestart` reopens a session
across a server restart.

## Client-provided tools at this pin

Whether `action.tools.provide` ([Decision 0011](../decisions/0011-control-layer-provided-tools.md))
could be served natively, so that a host's own tools reach an OpenCode session.
Read from the source at `v2.0.32`; not exercised against the binary.

**No client-tool API; possible only by hosting an MCP server.** OpenCode's
HTTP API has no route that registers a tool whose calls come back to the
client. `packages/core/src/tool/AGENTS.md` says built-ins, plugins and MCP
"install tools through `Tool.Service.transform`", and it forbids "a second
executable entry type". Plugins run inside the OpenCode process, and
`POST /api/rpc/:rpcID/:method` only calls into a registered plugin, so neither
lets a client execute a call.

MCP is the remaining route. `PUT /api/experimental/mcp/:server`
(`packages/protocol/src/groups/mcp.ts:24`, "Add an MCP server at runtime or
replace an existing one, connecting it immediately") takes `Mcp.ServerConfig`:
`local` with `command` and `environment`, or `remote` with `url` and
`headers`, each with an optional `timeout`
(`packages/schema/src/mcp.ts:26,53`). The adapter could serve the provided
tools as an MCP server, a loopback `remote` URL or a `local` command it
controls, and answer each `tools/call` from the OAP resolution.
`packages/core/src/tool/mcp.ts` registers each MCP tool as
`<server>_<tool>`, with non-alphanumerics replaced by `_`. It asserts the
permission `<server>_<tool>` before every call, which the adapters already
project as `action.permission.requested`, and then calls the server.

Two properties make the mapping degraded:

- **Scope.** MCP servers and the tool service are scoped to a location (one
  directory), not to a session (`AGENTS.md`: "`Tool.Service` is
  Location-scoped"). A provided tool would be visible to every session in that
  directory, including sessions another client opened. Decision 0011 provisions
  tools for one session's lifetime.
- **Stability.** The runtime route is under `/api/experimental/`. The other way
  to add a server is the `mcp` key of the service config at start, which the
  adapters do not write.

What `action.tools.provide` would map to:

| OAP | OpenCode |
|---|---|
| `tools` on `session.open.request` | an MCP server the adapter hosts, added with `PUT /api/experimental/mcp/oap-<session>` and listing exactly those tools |
| `action.call.requested` | the MCP `tools/call` that server receives, after OpenCode's permission assertion |
| `action.call.resolve.request` | the MCP `CallToolResult` the server answers: `content` text, `isError` for a failure |
| the five refusal reasons | the adapter's own |
| limits | MCP's tool-name rules, the server `timeout`, and the location scope above |
| session close | `DELETE /api/experimental/mcp/oap-<session>` |

**Recommendation.** Feasible only as `degraded`, because the tools are
location-scoped. It also needs an MCP server inside the adapter, the most
machinery of the three harnesses. Worth it only if OpenCode sessions need the
daemon's tools before OpenCode grows a session-scoped tool API.
