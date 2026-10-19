# Decision 0060: A Tool Result May Carry Content Parts

Status: proposed (the owner approved it on 2026-10-19; it becomes accepted
once the three trees judge it, with fixtures, and `oapx` reports image
results, under Decision 0003's gate)
Date: 2026-10-19
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Extends: [Decision 0001](0001-agent-control-v0.1-executable-core.md) and
[Decision 0011](0011-control-layer-provided-tools.md) without amending either

## Context

The core vocabulary defines an `image` content part, and `content.image` is a
feature key, but the two places a tool's outcome travels, a `tool_result`
part's `result` and `action.call.completed`'s `result`, are free JSON, and no
decision said whether an image may sit inside one. Nor did one say it for
`action.call.resolve.request`'s `result`, by which a control layer answers a
call to a tool it provides, and which the endpoint then reports on
`action.call.completed` (OPEN-0011-2). A tool that reads an image file has one
to report. `oapx`'s own loop keeps such an image and sends it to
the provider, but over OAP it reports only the result's text, so an observer
never sees what the model saw. The alternatives were to keep results text and
JSON, leaving images off the wire, or to keep saying nothing.

## Decision

### A result is JSON or a content-part list (OPEN-0011-2)

A `tool_result` part's `result`, `action.call.completed`'s `result` and
`action.call.resolve.request`'s `result` may each be a content-part list: a
non-empty array whose every element is a `text` or an `image` content part, in
the form a message's content uses. Such a result is
read as content, in order. Any other value is a JSON result, as before, so
existing results keep their meaning.

### An image part needs `content.image`

An `image` part in a tool result is valid only where the endpoint's descriptor
advertises `content.image` at a level other than `unavailable`. An endpoint
that reports one otherwise is `unavailable_capability`. A request carrying one
to such an endpoint is refused like any request exercising an unadvertised
feature. `content.image` is the key the core already names for images, so
one key tells a reader whether images reach it.

## Consequences

- `oapx` reports a tool result holding an image as a content-part list on
  `action.call.completed`, and its transcript keeps image parts in tool
  results rather than dropping them.
- Go, Zig and Rust judge the gate, with fixtures for an image result where
  `content.image` is advertised, where it is not, and a text-only part list.
- A control layer providing a tool can return an image through
  `action.call.resolve.request`, so the endpoint can report on
  `action.call.completed` what the control layer supplied.
