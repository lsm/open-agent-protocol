# Hermes agent v0.23.1 mapping ledger

Status: the implementation contract for the Hermes adapter pinned to
`v0.23.1`. It records only what changed against
[`hermes-v0.22.2-mapping.md`](hermes-v0.22.2-mapping.md), which with its
predecessors stays the base for everything not named here. v0.23.0 ("the
Hearth release") rebuilt much of the product around a single gateway that
owns each session, with the CLI, TUI, Desktop, editors, bots and cron as
clients of it; this ledger records what that means for the standalone stdio
gateway this adapter launches.

## Provenance

- Repository: `https://github.com/NousResearch/hermes-agent`
- Release: `v0.23.1`, published 2026-11-10 (annotated tag object
  `b65acf3fdc1d43bd889211ea8e37032974b395a5`)
- Release commit: `3b421578588243f920886e6fe79a6faa87ea026e`, tree
  `c69ce0f0fb52a2650efcbb79cc97d35474a9be9a`
- Contracts tree: `4bdc4e3f9e5e56923028558497a8c086348858a9`
- The release ships no binary artifact, so the catalog records no digest.
  `pyproject.toml` requires Python `>=3.11,<3.15`; the machine available for
  this comparison had CPython 3.15.2 only, so the checkout could not be
  installed and run. Every difference below was established from the source
  and contracts at the two tags, not from a running gateway: the pydantic
  contract dump and frame validation of the v0.22.2 method, and the live
  process gates, were **not** run for this pin.

Normative source blobs at the release commit:

- `tui_gateway/entry.py` — `ff2f9a899d33fc4466df85c4988e352b7c435228`
- `tui_gateway/transport.py` — `691f1d799027b211a073c685fbd7ee5d65fae3a6`
- `tui_gateway/server.py` — `c3cc3d2cd6894f07092b7c51aa1d0ba6f2aabdcb`
- `tui_gateway/event_replay.py` — `20298bd23f80922c02d7b45a3c920716021f290b`
- `tui_gateway/_stdin_recovery.py` — `add5e841df4bead308a96bd8b0f0e3c5475bd167` (unchanged)
- `tui_gateway/ws.py` — `833751e0df038b0c551814ee3bfea95a27d0dabf`
- `tui_gateway/methods_prompt.py` — `0b2baf072e82ecfeb8a06eb6dc97322941ad14dc`
- `tui_gateway/methods_session.py` — `5b1ca479044dc3c34e562ede151a25e34b9b229b`
- `tui_gateway/server_requests.py` — `7d089f8ad2701f8b220ffc061d2872cf6ecf13ef`
- `tui_gateway/rpc_dispatch.py` — `1f8d451f237b7b30cbeffa6c39324d2129128c6e`
- `tui_gateway/prompt_turn.py` — `c5658bfb36a4000b8d7e3a85c0859a712e816593`
- `tui_gateway/contracts/` (tree) — `4bdc4e3f9e5e56923028558497a8c086348858a9`
- `hermes_state.py` — `fe0249d6804ac5e6810a208f7a32e6dcf3e251f9`

## The stdio entry survives as a dev surface

`python -m tui_gateway.entry` still serves the same stdio RPC. Its
docstring now says no product surface launches it any more (Ink and Desktop
attach to the profile's gateway over WebSocket); dev harnesses still run it
against a sandbox home. It gains a `gateway.lock` ownership reservation
(`_claim_launch_home`): beside a live gateway on the same home it refuses and
exits, and Windows gets a private RPC stdin. The adapter launches the
gateway with an isolated home, so the reservation does not change its path;
the posture change (product surface to dev surface) is upstream's, and a
future pin may need to follow the product onto the shared gateway.

## Wire differences against v0.22.2

Read from the contracts at both tags (the pydantic sources under
`tui_gateway/contracts/`, compared structurally):

- `message.delta` extends `StreamDeltaPayload` with optional `turn_id`,
  `segment` and `offset`, and its `text` may now be `null`: a session-authority
  local viewer publishes a `text: null` segment boundary. Go's `DeltaPayload`
  decodes all three members and already drops an empty text chunk, which is
  the boundary's meaning for this adapter; Zig reads members dynamically and
  does the same. No OAP projection changed.
- `message.start` gains a payload (optional `display_kind`, `display_text`,
  `notification_category`, `text`, `turn_id`): an automation admission's
  async-result card. It is empty for the TUI gateway's own emitters and an
  ordinary admission. Go admitted `message.start carries no payload` before;
  it now decodes the card's members strictly and projects none of them. Zig
  ignores the payload.
- `message.interim` seals gain optional `turn_id` and `segment`;
  `message.complete` gains optional `user_row_ids` and `submission_id`
  (session-authority ack evidence). All decode and are not projected.
- `session.usage` makes `usage` nullable and adds the deprecated top-level
  counters (tokens, context, latency, rate limit, subagents, model, plan
  gauge) that a canonical viewer's gateway frame also carries; the embedded
  ticker this adapter serves never sends them. Go and Rust decode the
  members; nothing is projected from a tick.
- Events added: `agent.print` (a display line the agent printed),
  `skills.changed`, `tool.progress`. Events removed upstream:
  `browser.controller.cancel`, `browser.controller.command` — Go keeps both
  admitted, since an older gateway may still send them, as with
  `layout.apply` at v0.22.2.
- `session.events.since` params gain optional `replay_epoch`, accepted and
  ignored: legacy seqs are per process, and the epoch comparison against
  `gateway.ready` governs replay. The adapter's existing epoch handling is
  unchanged; `SessionEventsSinceResult` is byte-identical at both tags.
- `session.list` items gain optional `source`; the adapter decodes list
  results leniently. `session.close`, `prompt.submit`, `session.resume` and
  the other methods the adapter calls are unchanged in the contracts
  compared; the clarify answer gains optional `outcome` (the adapter answers
  without it); client capabilities gain optional `session_authority`, which
  the adapter does not send. New `clientSession.memoryStore` and
  `clientGlobal.network` methods and the `canonical_*` contract files are
  the session-authority surface, not this adapter's.
- `methods_prompt.py` rescopes voice chat to a session and splits the typed
  stop-phrase check into a helper the canonical path shares; the stdio
  path's `prompt.submit` behavior is unchanged in shape.

## Corpus

`fixtures/adapters/hermes-v0.23.1` carries the v0.22.2 cases forward
unchanged in what they replay, with provenance moved to the v0.23.1 release
commit, tree and source blobs, and every expectation carrying
`hermes-v0.23.1-oap-v1`. No case was re-recorded. No corpus case exercises
the new delta turn identity, the message.start card, the deprecated usage
counters or the new events; those are covered by the adapter unit tests
added with this pin. No account, credential, or model call was used.

## Unverified

No Hermes gateway was installed or run for this pin (Python constraint
above), so the live gates, a real prompt round trip, the ownership
reservation's refusal path, multi-layer subagent settlement and the
canonical session-authority wire remain unverified at v0.23.1. The
`copilot`-on-PATH caveat of the v0.22.2 ledger is unchanged in the source
compared here.
