# Decision 0056: Input Answers Are Judged Against the Prompt

Status: accepted 2026-10-19
Date: 2026-10-19
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Units: `user-input` (claim term `+user-input`) and `permissions` (claim term
`+permissions`)
Amends: [Decision 0001](0001-agent-control-v0.1-executable-core.md)'s
correlated reverse interactions

## Context

A `user.input.requested` prompt carries structured questions, and the core
draft says only that each is plain text, single choice or multi choice. Every
tree nevertheless judged the answers against the prompt, and adapters validate
them the same way before any native write, so the rule existed everywhere
except in the protocol (OPEN-0001-6).

## Decisions

### An answer answers the prompt it resolves

A `user.input.resolve.request` that submits answers is held to the prompt its
interaction carries:

- each answer names a question the prompt offered;
- each question is answered at most once;
- every question the prompt marks `required` is answered;
- an answer to a required question is not empty: a text answer carries text,
  and a choice answer selects at least one option;
- an answer to an optional question may be empty;
- a choice answer selects only options its question offered, and a
  single-choice answer selects at most one.

### A permission choice is one the prompt offered

An `action.permission.resolve.request`, and the `action.permission.resolved`
event, name in `choice_id` a choice the permission prompt offered.

A resolution that breaks any rule in either section is `unmatched_interaction`,
whether it is the request a control layer sends or the resolution event the
endpoint publishes.

## Consequences

The trees stop requiring text on an optional question's answer, and stop
requiring a selection on an optional choice question's answer. Everything else
they already judged.
