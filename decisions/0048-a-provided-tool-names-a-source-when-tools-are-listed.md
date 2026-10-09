# Decision 0048: A Provided Tool Names a Source When Tools Are Listed

Status: accepted 2026-10-08 (the owner chose this over exempting source-less provided tools from attribution, and over the endpoint attributing them itself)
Date: 2026-10-08
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `control-tools`, `tool-sources`
Amends: [Decision 0011](0011-control-layer-provided-tools.md), where a
provided tool's `source` is optional
Leans on: [Decision 0008](0008-tool-sources.md), whose served catalog
attributes every tool to a source the same response declares

## Context

Two accepted rules could not both hold for one tool.

- Decision 0011 lets an open supply a tool with no `source`, and requires every
  later catalog to describe a provided tool exactly as it was supplied, `source`
  included (`catalog_mismatch`).
- Decision 0008 has a served `action.tools.list.response` attribute every tool
  it lists to a source the same response declares; a served catalog that
  attributes no source is one of its negative cases (`unmatched_tool_source`).

An endpoint that admitted a source-less provided tool and served a tool listing
therefore broke one rule or the other: listing the tool without a source broke
0008, and listing it under a source broke 0011. Both validators reported it, and
it surfaced when the `oapx` endpoint's listing was first checked end to end
(#1012). No endpoint could satisfy both.

## Decisions

1. **An endpoint that advertises `action.tools.list` requires every provided
   tool to name a source.** The source is one the capability descriptor
   declares or the same open attaches, as Decision 0011 already requires of a
   source that is present. An endpoint that does not advertise
   `action.tools.list` keeps 0011's rule unchanged: `source` stays optional,
   because nothing it serves has to attribute the tool.
2. **The refusal is 0011's typed one.** An open supplying a source-less tool to
   a listing endpoint is refused `unsupported_feature` with
   `details.feature: "action.tools.provide"`, `details.reason:
   "unsatisfiable"`, and `details.tool` naming the tool. There is no source to
   name, so `details.tool` is the detail.
3. **The diagnostic is `unmatched_tool_source`**, on the
   `session.open.response` of an open admitted despite the rule, and on a
   refusal under another code or without `details.tool`, which is how 0011
   already validates a dangling source.
4. **A control layer names a source the endpoint declares.** One that has no
   source of its own reads `sources` from `capabilities.response` and names
   the endpoint's native source, or its first declared source when none is
   native. That is the same attribution a provided tool naming the
   descriptor's `native` source already had in the fixtures.

## Consequences

- Both validators check the rule at admission, so a trace that ends after the
  open cannot carry a provided catalog entry that the listing could not
  attribute. Fixtures: `open-provide-no-source-listed` (the typed refusal),
  `open-provide-no-source-unlisted` (admitted, because nothing is listed),
  `open-provide-no-source-listed-admitted` and
  `open-provide-no-source-listed-wrong-refusal`.
- The Go and Zig memory adapters and the `oapx` adapter refuse a source-less
  provided tool, since all three advertise `action.tools.list`.
- The Go, TypeScript, Python and Rust SDKs name the endpoint's declared source
  on every tool they provide (#1016), and landed first so no SDK was refused.

## What this decision does not admit

- It does not let an endpoint attribute a provided tool on the control layer's
  behalf. The catalog still describes the tool exactly as supplied.
- It does not exempt provided tools from Decision 0008's attribution rule.
