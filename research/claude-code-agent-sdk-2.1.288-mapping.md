# Claude Code / Claude Agent SDK 2.1.288 mapping ledger

Status: the Claude Code adapter's pin. This ledger moves the adapter from
2.1.282 to 2.1.288 and records only what the move changed or settled.
[The 2.1.282 ledger](claude-code-agent-sdk-2.1.282-mapping.md),
[the 2.1.280 ledger](claude-code-agent-sdk-2.1.280-mapping.md) and, beneath
them, [the 2.1.263 ledger](claude-code-agent-sdk-2.1.263-mapping.md) remain
the mapping of record for every surface not restated here. The adapter's
corpus is `fixtures/adapters/claude-code-2.1.288`. 2.1.282 is retired: its
corpus is removed, because its expectations name a capability revision the
adapter no longer advertises. Every hash below was computed on 2026-10-03 from
the artifacts named, and the live checks ran the 2.1.288 darwin-arm64 binary.

## Provenance

### Claude Code CLI 2.1.288

- npm artifact: `@anthropic-ai/claude-code@2.1.288`
  - tarball sha256: `13ad0dd7d50855682082e8d0c4d4d45822170cbb5bebde5f4d024611a0c94842`
  - tarball sha1 (npm `dist.shasum`): `db23b6403d85545e4575e97dbfdfbc5bb745f598`
  - npm `dist.integrity`:
    `sha512-tnc8XuK5xQkyE6oowhhSPIjLuNc0S3MkjIHqycDsKP6yJlLtIjMt6v1TOkYLUiumcZ9jOgJZ6h9Vn3/f0murdQ==`
  - the wrapper tarball is 28,470 bytes; `cli-wrapper.cjs` is byte-identical
    to 2.1.282's (sha256
    `61ad63033d9c8155d5e60a29f45dc4665afa07631c0b108e62cc83bf45ba490e`)
- native linux-x64 artifact: `@anthropic-ai/claude-code-linux-x64@2.1.288`
  - tarball sha256: `b54d6a2c6bdd78f2c83139d1223598641686aab9e562c47cf537dd200653fa90`
  - tarball sha1: `163e1c8894e1b3db733fdd81ae6edf5288e9a6c3`
  - npm `dist.integrity`:
    `sha512-m80cJZimlKjRiwveadPgAfubhbmwa2nF85gg20WbjnHl6QKMad0bXiMp6JNcPeSZsr0N1KJ/wkR34rRoiyyYyg==`
  - binary sha256: `0298068b686e7fdbaf9402a7a587bb7f49c0b0e084de09f69145a0719207640c`
  - binary size: 245,734,584 bytes
- native darwin-arm64 artifact, the binary the live checks below ran:
  `@anthropic-ai/claude-code-darwin-arm64@2.1.288`
  - tarball sha256: `7cb1cbd826e6aa5ffc347e15193776c59373f840f40ec0fe3acfdcae342864da`
  - tarball sha1: `8095f7c379eeacc17c7aefd459255e053ec04795`
  - npm `dist.integrity`:
    `sha512-kioqJixZJY87Dgoog1VAHxPo+5h0XrDTRFLmZzRKxfjfEGF6aKv+Zt1OqJUfeUnD3+VUr+iqemRZ5vPTg2gGeQ==`
  - binary sha256: `bbe93063f7a0879a1021b2891e5c9354e5b3b98433e32efe6750f7710afed750`
  - binary size: 229,255,312 bytes
  - `claude --version` self-reports `2.1.288 (Claude Code)`
- build manifest (carried by the Agent SDK artifact, `manifest.json`):
  - CLI build commit: `17fe1eb736e5b1433d6ca86a1db334cec8520450`
  - build date: `2026-10-02T17:00:28Z`

Every npm tarball above was checked against its registry `dist.integrity` on
download.

Reproduce:

```sh
curl -fsSL 'https://registry.npmjs.org/@anthropic-ai%2fclaude-code/-/claude-code-2.1.288.tgz' -o claude-code-2.1.288.tgz
curl -fsSL 'https://registry.npmjs.org/@anthropic-ai%2fclaude-code-darwin-arm64/-/claude-code-darwin-arm64-2.1.288.tgz' -o claude-code-darwin-arm64-2.1.288.tgz
shasum -a 256 claude-code-2.1.288.tgz claude-code-darwin-arm64-2.1.288.tgz
tar -xzf claude-code-darwin-arm64-2.1.288.tgz
shasum -a 256 package/claude # bbe93063f7a0879a1021b2891e5c9354e5b3b98433e32efe6750f7710afed750
./package/claude --version   # 2.1.288 (Claude Code)
```

### TypeScript Agent SDK 0.3.288

- public repository: `https://github.com/anthropics/claude-agent-sdk-typescript`
  - tag `v0.3.288`, commit `36836f000b931056f7de6471bcc90b31d658d4e9`, tree
    `7f794be4aa099eb315fe6a42edcd07134bf213e1`; provenance, not runtime
    evidence
- npm artifact: `@anthropic-ai/claude-agent-sdk@0.3.288`
  - tarball sha256: `eb97c0f5a7d96189bceaf1986c2751a024b7e13844ba843531cc55bb0b4fac54`
  - tarball sha1 (npm `dist.shasum`): `04e7c68249a03bc2c43c10a62acc56f5792a44cd`
  - npm `dist.integrity`:
    `sha512-W0axvKSBKC8E1rMvriV8NVMFfNv8uwN3HnJxUiFLJOQcJLp1MDp0SoAugkAKTn3WLx3JnqCxxME6bGvfNJzJoA==`
  - `package.json` declares `claudeCodeVersion: "2.1.288"`
  - file hashes inside the artifact:
    - `sdk.mjs`: `52dd9e10c1d1bde4cd53d2bcb035e6ae75123b28edafb1fba208552bdc679a10`
    - `sdk.d.ts`: `52d1c2c93ecb35a301044319f60e10d365f8e1ea0f1812054e1d4b4b2f183fb7`
    - `manifest.json`: `9a2acd1052522ebacd592dfddac56075782ce02a7316b66d82d1258c7799a48b`

### Python Agent SDK 0.2.163

No Python release bundles 2.1.288. The newest, `v0.2.163`, bundles 2.1.286
(`_cli_version.py` blob `7804e1d64ee9039e9d482a8af89e0634418e86ba`). It is
pinned because it is the closest.

- repository: `https://github.com/anthropics/claude-agent-sdk-python`
  - tag `v0.2.163`, commit `1ef6d8c71bb0e44a6b33fe61497864f21e17fdb7`, tree
    `c621a9b571c58e64f6f17baddcae4c26e4ae2f57`
  - `pyproject.toml` blob: `676ce6a0dc44f37ab9dbf08b76bac8ad983f7ac3`
- between `v0.2.159` and `v0.2.163` the normative sources `client.py`,
  `types.py`, `_internal/query.py` and `_internal/transport/subprocess_cli.py`
  changed; `message_parser.py`, `session_resume.py`, `session_store.py` and
  `session_store_validation.py` did not. Per the changelog, 0.2.160 keeps
  stdin open after a turn's `result` until the CLI's `session_state_changed`
  reports `idle`, so a background subagent finishing before the result no
  longer breaks a follow-up turn; the others only move the bundled CLI. The
  new blobs are in `manifest.json`.

## Wire delta from 2.1.282

`sdk.d.ts` between 0.3.282 and 0.3.288, for the frames this adapter decodes:

- `system/init`: optional `plugin_errors`, absent when every plugin loaded;
- `result`: optional `first_text_post_queue_wait_ms` and
  `first_text_post_queued_behind` timings;
- a system notice line: optional `tag`;
- `initialize` response: optional `sdk_mcp_manifests_parked`, which answers a
  request member this adapter does not send;
- new control requests the adapter does not send (`get_task_output`), new
  startup failure reason `provider_not_allowed`, and settings documentation
  (`autoCompactWindow` per model may be `"auto"`).

None of it names a member the native decoder requires or projects, and both
decoders ignore members they do not name.

## Live verification at 2.1.288

`TestClaudeProcessRecordsCorpusProbes` ran all seventeen probes against the
2.1.288 binary and, the same hour with the same method, against the 2.1.282
binary, so the two captures differ only by the release. Method as in the
2.1.280 ledger's *Capture*: a `security` stand-in first on `PATH` (every
lookup it logged was `find-generic-password` for `Claude Code-<hash>` of a
temporary `CLAUDE_CONFIG_DIR`, and it answered not-found), `sandbox-exec`
denying the keychain daemons and every non-loopback connection, and the proxy
sink. The sink saw no connection in either run, and every probe passed.

Compared frame by frame, after removing ids, paths, timings and costs:

- Every probe emitted the same frame kinds in the same order.
- Members added at 2.1.288, all optional: `usage.fallback_credit` on
  `assistant` and every `result`; `first_request_input_tokens` and
  `origin.producer` on some `result` frames; `thinking_display` on
  `stream_event`; `capabilities` and `feedback_mode` on the `initialize`
  response. None was removed.
- `system/init` gained no member; its harness-written values changed:
  `claude_code_version` is `2.1.288`; `capabilities` adds
  `interrupt_send_now_v1`, `sdk_mcp_tools_list_changed`, `sdk_mcp_manifests`
  and `ui_surface_v1` to the five 2.1.282 reported; the built-in plugins are
  renamed `cc-plugin-agents-md` and `cc-plugin-plugin-authoring`, and
  `skills` and `slash_commands` gain `plugin-authoring`.
- The OAP the adapter emitted is identical between the two runs apart from
  ids, timestamps, the capability revision and the random task and agent ids
  inside three tool results.

The Go process gates (`OAP_CLAUDE_SMOKE=1`, `OAP_CLAUDE_INTEGRATION=1`) pass
against 2.1.288 under the same sandbox, and `oapx serve agent --backend
claude`, with `claude` on `PATH` a wrapper pointing the binary at the loopback
Messages mock, completes a run with the fixture text under
`claude-code-2.1.288-oap-v1`.

## What changed in the adapter

Nothing in the adapter code of either tree. The catalog moves the pin, and
with it the endpoint version `v2.1.288` and the capability revision
`claude-code-2.1.288-oap-v1`. The advertised features are the
`claude-code-2.1.282-oap-v2` revision's.

## Corpus at 2.1.288

`fixtures/adapters/claude-code-2.1.288` holds the 2.1.282 corpus's thirteen
cases, carried forward, because the captures show the wire did not change for
any frame they carry except `system/init`'s values:

- **Re-derived from the 2.1.288 capture**: every `system/init` frame (23
  across the cases). Each keeps the members its 2.1.282 counterpart carried,
  with 2.1.288's values: `claude_code_version` `2.1.288`, and the 2.1.288
  `capabilities` and `slash_commands`, which every one of them carried at
  exactly 2.1.282's values. The members it never carried (`plugins`, `skills`,
  `view_mode` and so on) are still not added.
- **Carried forward unchanged**: every other native frame, script step and
  expectation. The new optional members above are not added to the frames
  that would now carry them, as the 2.1.282 move did not add its own.
- Expectations change only the capability revision. Provenance in
  `manifest.json` and every `case.json` names the 2.1.288 artifacts above.

## Live session settings at 2.1.288

Recorded for Decision 0045's live half. The 2.1.282 ledger left open whether
`apply_flag_settings` changes a running session; at this pin it is observed.

**Live probe.** The 2.1.288 darwin-arm64 binary, sandboxed as in *Live
verification* (no network, no keychain, a temporary `CLAUDE_CONFIG_DIR`, no
model call), took `initialize`, then alternating `apply_flag_settings` and
`get_settings`:

| sent `settings` | `get_settings` `effective` | `applied.effort` |
|---|---|---|
| (none) | none of the three keys | `medium` |
| `effortLevel: high, autoCompactEnabled: true, autoCompactWindow: 150000` | the same three | `high` |
| `effortLevel: low, autoCompactEnabled: false` | `low`, `false`, window still `150000` | `low` |
| all three `null` | none of the three keys | `medium` |

So the request merges into the flag layer of a live session, a key it omits
keeps its earlier value, `null` removes a key and returns it to the CLI's own
default, and the effort the CLI will send (`applied.effort`) follows each
change.

**In the adapters.** Both trees advertise `session.reasoning` and
`session.compaction.policy` with `session_live` beside `session_open` and
serve `session.settings.update.request` through one `apply_flag_settings`
carrying every named setting:

- a level sets `effortLevel`; `off` and `minimal` are refused unsatisfiable;
- `tokens` sets `autoCompactEnabled: true` and `autoCompactWindow`; `off`
  sets `autoCompactEnabled: false` and clears the window; `auto` clears both,
  because the window a previous update set would otherwise survive the merge;
  `share` is refused unsatisfiable;
- the update is refused `run_active` while a run is open. The CLI applies
  flags to its next request, which inside a run would change the level of the
  run already under way, and Decision 0045 keeps a running run at the level it
  started with.

`goap serve agent --backend claude` and `oapx serve agent --backend claude`,
each with the sandboxed binary as their child, answered two updates (`high`
with a 120000-token window, then `auto`) with the values asked for and the
ones replaced, followed by `session.state.updated`; `oapx` also refused
`minimal`, which the Go adapter's unit tests cover.
Whether a live `autoCompactWindow` moves the next compaction is still not
observed: that needs a model call.

The descriptor changed, so the revision moves to `claude-code-2.1.288-oap-v2`.


## Binding-based reopen (#448)

Both adapters select the binding's CLI UUID through `--resume <uuid>` on a
new process, retain the OAP session id and answer an idle state with
`recovery.recovered: true`. The readiness `initialize` exchange completes
after the conversation is loaded. `get_settings` then reports `applied.model`
and `applied.effort`, plus the effective `autoCompactEnabled` and
`autoCompactWindow`; these become the state document's model, reasoning level
and compaction policy. A null or unrecognised effort is left unspecified.
Recovery's reason explicitly says these settings belong to the loader:
Claude restores messages, not the former process's configuration.

`TestClaudeProcessReopensItsBoundConversation` live-verified the catalog's
Darwin ARM64 artifact (SHA256
`bbe93063f7a0879a1021b2891e5c9354e5b3b98433e32efe6750f7710afed750`)
through `adaptertest.VerifiedBinary`, with an isolated home/config directory,
a fixture API key and a loopback Messages mock. After a completed turn and
stdin-EOF close, the bound UUID reopened without a user turn. The next native
Messages request included both the old and new user messages. An absent UUID
printed `No conversation found with session ID: <uuid>` to stderr and exited
before answering initialize. Both trees translate that inability to load into
`unsupported_feature`, naming `session.open.reopen` with reason
`unsatisfiable`; an empty or invalid binding is refused before spawning.
No transcript file is read by the adapter and no earlier OAP event is replayed.

The new `session-reopen` corpus case records the native initialize and
get_settings exchanges captured by that gate; its native member names and
values are retained, including the bound UUID and per-process request ids.
Its expected state reports loader configuration rather than the stored
session's. The Go adapter replays the exchange and the Zig adapter consumes
its settings response through a fake child. The decoder corpus excludes this
case because a binding-based reopen is an adapter lifecycle operation; the
adapter test executes it instead. The capability is native, as conversation
reload is native; loader configuration is disclosed in recovery. The revision
advances to `claude-code-2.1.288-oap-v3`.

A caller-supplied Go `ClientFactory` has no way to receive the bound UUID, so
that injection path refuses reopen as unsatisfiable; process-backed hosts and
`ProcessFactory` receive the binding in argv. A configured resume, continue,
fork or session-id selector is likewise refused rather than overriding the
binding. The opt-in native gate stays out of CI and downloads nothing.


The normal process-backed create selects a secure random UUID with
`--session-id`, and both adapters expose it through the native-session getter
before a turn exists. This lets a hub record the binding at open, rather than
waiting for the first `system/init`; the real process gate checks the CLI keeps
that UUID on its first turn. An untouched session may have no transcript file
yet, so closing it before any turn does not make a native reload possible.
Legacy configured session selectors remain caller-owned on create, and their
UUID is only known once observed; they cannot override a binding-based reopen.
