# Decision 0058: Pack Declarations Are Definitions

Status: accepted 2026-10-19
Date: 2026-10-19
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `extensions` (T0)
Amends: [Decision 0004](0004-extension-packs.md)

## Context

Decision 0004 has a pack declare its envelope types, payload members,
responses and refusals, and both Go (`pack.go:LoadPacks`) and Zig (`packs.zig`)
refuse five ways of getting those declarations wrong:
`pack_type_duplicate`, `pack_member_duplicate`, `pack_reply_target_unknown`,
`pack_reply_target_ambiguous` and `pack_refusal_undeclared`. The decision's
text implies each and states none. The validator rule register drafted a rule
for each rather than copy it from the trees, and the owner adopted all five.
This decision writes them into 0004.

## Decisions

### One name has one declaration

An envelope type is declared once in `envelope_types`, and a payload member
once per core payload in `payload_members`. A declaration is a definition, so
two of one name would be two possibly disagreeing definitions with nothing to
choose between them. A repeat is `pack_type_duplicate` or
`pack_member_duplicate`.

### A response answers one request its own pack declares

A declared `response` names in `replies_to` a request the same pack declares.
The response has no gate of its own: it takes the gate of the request it
answers, which only a request this pack declares carries in this pack's
`gates`. A target that is not such a request is `pack_reply_target_unknown`.
A request has one response type, because correlation names one answer, so a
second response naming the same request is `pack_reply_target_ambiguous`.

### Refusals belong to a declared request and list declared codes

A type's `refusals` belongs to a request the pack declares, and every code it
lists is one the pack declares in `error_codes`. The honour rule bounds a
refusal by that list, so a code the pack never declared would be a refusal
reason nobody can look up. An undeclared code is `pack_refusal_undeclared`.

## Consequences

- Decision 0004's declaration, `role` and `refusals` clauses state these rules
  in their own text, and the register's 0004 rows cite them.
- Go and Zig refuse all five today. Neither refuses a `refusals` list on a
  response or an event; both owe that check, under
  `pack_refusal_undeclared`, with a fixture. Rust loads no packs and owes all
  five.
