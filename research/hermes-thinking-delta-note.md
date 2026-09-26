# Hermes `thinking.delta` is the activity spinner

This note amends the projection recorded in
[`hermes-v2026.8.31-mapping.md`](hermes-v2026.8.31-mapping.md). That ledger's
identifiers stay as written. From this note on, both adapters treat the
gateway's `thinking.delta` event as observed-only. `reasoning.delta` still
projects as a `content.delta` reasoning part.

## Evidence

The gateway emits `thinking.delta` from the agent's `thinking_callback`. At
`v2026.8.31` (`29112bef099274229cadff79cdff7bf7b99c4b77`),
`tui_gateway/server.py:8237` reads
`"thinking_callback": lambda text: _emit("thinking.delta", sid, {"text": text})`.

The agent passes that callback only a face-and-verb status string, or `""` to
clear it:

- `agent/conversation_loop.py:2984` at `v2026.8.31` calls
  `agent.thinking_callback(f"{face} {verb}...")`. Lines 3258, 3380 and 3395
  pass `""`.
- `agent/turn_iteration_prep.py:304` at `v2026.9.24`
  (`f97608f178d1ffeca59860195ab7da295f7c8e5f`) makes the same call.

Recordings of the real v2026.9.24 gateway, on branch `harness/upgrade-hermes`, show text such as
`(´･_･`) mulling...`, and an empty frame on every turn. This text is
presentation state, not model reasoning. Projecting it as reasoning put spinner
text into the reasoning stream a consumer renders or stores. The empty frames
were also invalid.

## Change

- Go `applyRunEvent` and the Zig reducer no longer map `thinking.delta`. The
  event stays run-scoped and is dropped, as `message.interim` is.
- `fixtures/adapters/hermes-v2026.8.31/streaming-provenance` frame 8 is
  reclassified `observed-only`, with `native` fidelity. The change is made in
  three places: the line's own annotation, `mapping.json`, and an omission in
  `omissions.json`.
- The `raw` member of every `native.jsonl` line is byte-identical.
- The regenerated `expected-oap.json` loses only the reasoning delta `"wait"`.
  The next envelope's id, sequence and timestamp shift up to fill the gap.
- The `reasoning-deltas` ledger fixture now counts `reasoning.delta` alone. It
  also requires every `thinking.delta` in the case to be observed-only.

The capability revisions are unchanged. The descriptor advertises no feature
tied to this event. The change removes a projection that was never a
capability, and it adds none.
