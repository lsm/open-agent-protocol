# Draft decision: Hermes gateway ownership

Status: proposed; awaiting human decision. This document does not change OAP
semantics or the tested Hermes pin, v0.22.2.

## Context and evidence

At OAP main `0f9cc372d14300686781b0e704ae78da1f033f10`, both adapters
start a `tui_gateway.entry` process for a session, with a separate retained
process for native session listing. Hermes v0.23.0 changes that boundary:
[`tui_gateway/entry.py` at v0.23.0](https://github.com/NousResearch/hermes-agent/blob/v0.23.0/tui_gateway/entry.py)
reserves the launch home's gateway ownership lock; it exits when a running
gateway or another entry owns that home. Its comment describes standalone
stdio as a development path and the product surfaces as gateway clients.
The [release notes](https://github.com/NousResearch/hermes-agent/releases/tag/v0.23.0)
describe a single gateway owning sessions. Thus independent processes sharing
one home cannot simply be upgraded while preserving multi-session and listing
behavior. This conclusion is from source, not a running-process test.

The [upstream comparison](https://github.com/NousResearch/hermes-agent/compare/v0.22.2...v0.23.0)
changes the TUI gateway in 95 files (8,341 insertions and 3,450 deletions),
including 20 contract files. In `contracts/events.py`, `gateway.ready` adds
optional `session_authority`; `message.delta.text` permits null and adds
optional turn and segment identifiers; `session.usage.usage` becomes optional.
`session.events.since` accepts an optional `replay_epoch`. These observations
are not a complete field compatibility audit. The canonical socket has
separate session contracts and must not be assumed equivalent to stdio.

## Options

1. **Retain a process per session with separate homes.** This avoids one lock
   collision but changes storage, credentials, session discovery and sharing;
   it cannot silently stand in for the existing shared home. Ownership,
   migration and access to stored sessions require a separate design.
2. **Use one shared gateway per home.** Attach sessions and listing to a
   single owner and demultiplex native events by session. Establish whether
   OAP starts an owned gateway or attaches to an existing one, and distinguish
   their shutdown and failure responsibilities. The canonical transport,
   authentication and field contracts need verification first.
3. **Keep v0.22.2.** Preserve the tested integration while the upstream
   ownership and transport contracts are investigated.

## Recommendation for decision

Choose option 2 as the direction for investigation, retaining option 3 until
its semantics and proof are accepted. Do not start or stop a user's existing
gateway without an explicit ownership contract. Reusing an external gateway
must not let closing an OAP session terminate it; an OAP-owned process must
outlive all its sessions and listing calls and be reaped when its owner closes.
Those are proposed requirements, not current guarantees. A crash must surface
failure to affected sessions, and reconnection must not fabricate continuity or
replay. Bound routing, admission, session identity, permissions, and cancellation
need evidence on the actual selected transport before implementation.

## Proposed slices and decision gate

First compare the full contracts and capture synthetic handshake, unchanged
fields, lock contention, multi-session routing, listing, shutdown and recovery
cases without credentials or model calls. Next agree on ownership and coordinate
any ACP interface through Neo. Then design independently mergeable Go and Zig
slices, with shared corpus agreement including Rust, followed by a pin change
only after the new version's proof is green. Re-measure implementation size
before committing to PR boundaries. No behavior change is authorized by this
draft. The human must decide the ownership model before such work starts.

Not verified: complete field-level compatibility, runtime lock behavior,
canonical transport authentication or lifecycle, corpus agreement at v0.23.0,
and a live model interaction.
