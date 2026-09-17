# OAP Composition: Layers, Profiles, and What A Client May Choose

Status: draft

This describes how OAP's layers compose, and what a client may say about the
layers below the one it talks to. It is the frame the individual decisions
assume and none of them states, which is why several were written as isolated
additions when they are parts of one shape.

## The layers

```
  presentation layer          UI, CLI, TUI
        |
        |  agent-control-core            profile, executable today
        v
  agent loop / control layer
        |                                  \
        |  model-provider-core              |  tool execution
        |  (decision 0016, proposed)        |  (sources, 0008; control-owned, 0011)
        v                                   v
  model provider                       tool executor
  OpenAI-compatible,                   process, local, remote,
  Anthropic-compatible                 hosted, MCP, control-layer
```

An agent loop sits between three boundaries. Above it, a client drives
sessions and runs. Below it, two different kinds of thing: models that produce
tokens and tools that do work. The loop organizes the turn — request the
provider, receive tool calls, dispatch them, return results, continue — and
streams the result upward.

The layers are logical, not deployment sides. All of this can be one process,
or a client in a browser, a loop on a server, tools on a third machine, and a
model behind a vendor's API.

**How the loop is built is not the protocol's business, and both ways are
ordinary.** A loop may wrap a vendor SDK and expose `agent-control-core` above
it; that is what most pinned harnesses do. Or it may speak to inference
endpoints directly and expose the same profile; that is what Makai does. The
upper boundary is identical. The lower one is where they differ, and
`model-provider-core` is what makes it a boundary rather than an
implementation detail.

## What a client may choose

The point of layering is that the client is not stuck with whatever the loop
happens to prefer. A client should be able to say which tools, from where, and
which model, from which provider — and an endpoint should be able to refuse
any of it, typed, without the client having to guess why.

For **tools**, that story is complete:

| Concern | Surface | Decision |
| --- | --- | --- |
| Where tools come from | `session.open.request.tool_sources` — kind, protocol, endpoint | 0008 |
| Tools the client supplies and runs itself | `session.open.request.tools` | 0011 |
| What exists in this session | `action.tools.list` | 0008 |
| Which tools this run may use | `submit.tool_choice` | 0005 |

For **providers**, it is a third of that:

| Concern | Surface | Status |
| --- | --- | --- |
| Where inference comes from | `session.open.request.providers` — id, wire, kind, endpoint, environment | proposed in 0017 |
| What exists | `models.list`, with `provider_id` as a bare label | 0006; descriptors proposed in 0014 |
| Which model this run uses | `submit.model_id` | 0005 |

That row is the one a client notices, and until 0017 it was empty. A client can
say "use this MCP server over stdio" and could not say "use this provider" —
and "use this SDK with that provider" is the same request seen from the loop's
side.

## The shape the missing row should take

[Decision 0017](../decisions/0017-provider-provisioning.md) now writes this
row. What follows is the frame it was written against, kept because the
reasoning is the draft's and not that record's.

Tool sources answer it already, and the provider case should mirror them
rather than invent a second pattern.

Provisioning is an **attachment at session open**, a `providers` array beside
`tool_sources`, not a writable field on a descriptor. The distinction matters:
a descriptor is what an endpoint publishes about itself and is fixed for a
capability revision, while an attachment is what a caller asks for and is
judged at admission. Decision 0014 initially put a caller-supplied `endpoint`
on the descriptor, which conflated the two.

It inherits the tool-source rules unchanged, because they were written for
exactly this hazard:

- Gated on a capability key; an endpoint that does not advertise it refuses
  with a typed `unsupported_feature`.
- Refused whole or admitted whole, with the refusal naming the entry at fault.
- `oap serve` accepts an **id** naming operator configuration. `serve/attach.go`
  today refuses a command or arguments outright, refuses a process source whose
  id is not configured, and refuses any wire member — `endpoint` among them —
  that contradicts the configured source it names. A non-process attachment
  whose id is unconfigured still passes through carrying its own endpoint, so
  the destination is governed for configured sources rather than forbidden
  everywhere. A unit extending this shape to prompts would have to close that
  gap deliberately, for the reason the rule exists: a control layer that may
  not choose which binary runs a tool may not choose which host receives a
  prompt.
- Credentials never travel. Environment carries the bare `NAME` allowlist
  form; the operator supplies values. A `NAME` allowlist is necessary and not
  sufficient: any member passing caller text through to the upstream request is
  a credential channel whatever it is called, which is why 0017 excludes
  headers rather than relying on having declared no credential field.

An in-process embedder whose control layer *is* the operator may accept more,
which is what makes bring-your-own-key and per-session gateways expressible
without the daemon relaxing anything.

One rule does **not** come across with the rest, and the mirror is where it
hides. Tool sources are per-session in every implementation that carries them,
so attaching one at session open needs no scoping argument. Provider resolution
is not per-session everywhere: an agent loop may build its provider registry
once per process, before any session exists, in which case a session-scoped
attachment has nowhere to live. 0017 makes that a refusal rather than a merge,
for the reason the whole layer exists — a provider silently shared between two
sessions sends one caller's prompts to the other caller's endpoint, and nothing
on the wire says so.

## Why this is one shape and not four decisions

Each boundary needs the same three things, and reading the decisions
individually hides it:

1. **A protocol for the boundary.** Agent control has one. Tools have MCP and
   the source kinds. Providers have 0016, proposed.
2. **Discovery** — what is available here. `action.tools.list`; `models.list`
   plus 0014's provider descriptors.
3. **Provision and selection by the client** — attach a source, supply a tool,
   choose a model. Complete for tools; proposed but not executable for
   providers, since 0014 and 0017 are both proposed and neither has an
   implementation outside this repository.

A capability absent from one column is not a small gap. It is the layer below
becoming something the client cannot see or steer, which is the thing layering
was for.

## What stays out

The agent loop's own strategy — how it decides to call a tool, when to stop,
how it assembles context — is not a boundary and has no protocol. OAP
describes what crosses between layers, not what a layer decides.

Vendor APIs beyond one inference call: batching, embeddings, fine-tuning,
files. An implementation may use them; they are not the loop's lower boundary.
