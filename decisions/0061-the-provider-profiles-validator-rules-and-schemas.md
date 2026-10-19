# Decision 0061: The Provider Profile's Validator Rules and Schemas

Status: accepted 2026-10-19
Date: 2026-10-19
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.model-provider-core`
Amends: [Decision 0016](0016-model-provider-profile.md)

## Context

Decision 0016 defines the model-provider boundary and leaves "its schemas and
validator rules" to the next decision. No later decision took them up. Go and
Zig nevertheless judge provider traces against
`schema/v0.1/provider-envelope.schema.json`, the schemas it references, and six
semantic rules, all written in
[the model-provider-core draft](../drafts/model-provider-core.md), which is a
draft rather than a decision. The two trees agree on every one; Rust has no
provider validator. The validator rule register kept each as an open question,
numbered as in the headings below, rather than let the trees' agreement stand
in for the protocol's. Each section states its question and the answer, so
this decision is read without any other record.

## Decisions

### A streamed terminal is the assembly of its ended parts (OPEN-0016-1)

What must a streamed `inference.completed` say? Its `message` is the assembly
of the inference's ended parts in `part_index` order, and a `part.ended` is
authoritative over the deltas before it. A trace that streamed parts and whose
terminal is not their assembly is `terminal_not_assembly`. A trace with no part
envelopes is outside the rule, so a unary implementation stays conformant.

### A credential in headers is refused by name and by value (OPEN-0016-2)

What does the validator treat as a credential in `headers`? A header named
`Authorization`, `Proxy-Authorization`, `X-Api-Key` or `Api-Key`, matched
without regard to case, or any value that begins with `Bearer `, also without
regard to case, after leading spaces and tabs are trimmed. Either is
`credential_in_headers`, on an `inference.create.request`'s `headers` and on a
descriptor's published `headers` alike. The predicate is a floor the validator
can check, not a detector of every secret; a credential belongs in the grant.

### A trace never carries a granted credential (OPEN-0016-3)

May a trace carry `provider.credential.grant.request`'s `value`? No. A grant
request carrying `value` makes the trace invalid with `credential_in_trace`,
because the stream it was assembled from recorded a secret, and that envelope
is judged no further. A grant exchange that carries a reference rather than a
value is traced normally.

### Each inference has its own contiguous sequence (OPEN-0016-4)

How is a provider event's `sequence` ordered? Every event scoped to one
inference carries a positive `sequence` in that inference's own domain, opening
at 1, contiguous, and never repeating or going back. A skipped value is
`sequence_gap`, and a repeated or earlier one `sequence_regression`. Requests and
responses do not consume it.

### Nothing follows an inference's terminal (OPEN-0016-5)

What may follow `inference.completed` or `inference.failed`? No event scoped to
that inference, a second terminal included; one that does is
`event_after_terminal`.

### A refusal allocates no inference (OPEN-0016-6)

May a refused `inference.create.response` name an `inference_id`? No: the id is
present only when `accepted` is true, so an inference exists exactly when it
was accepted and never owes a terminal it cannot get. The schema already
enforces this, so a refusal carrying an id is `schema_invalid`.

### The provider schemas on main are normative (OPEN-0016-7)

Are the provider profile's schemas part of the protocol? Yes:
`provider-envelope.schema.json` and the schemas it references, as they stand on
`main` at this decision, are normative for `model-provider-core`, and a schema
violation is `schema_invalid`. A constraint in them that the draft turns out
not to support is not grandfathered by this; it becomes an open item of its own
and is settled by a later decision.

## Consequences

- Decision 0016's deferral now names this decision, and the draft's rules
  above are the profile's rules rather than a proposal.
- Go and Zig already judge all seven. Rust owes a provider validator for them,
  with the provider corpus under `fixtures/provider/` as its acceptance test.
