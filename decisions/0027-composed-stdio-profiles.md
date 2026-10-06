# Decision 0027: Compose Profiles on One stdio Connection

Status: proposed
Date: 2026-09-22
Protocol: `open-agent-protocol` version `0.1`
Profiles: `open-agent-protocol.agent-control-core`,
`open-agent-protocol.model-provider-core`
Amends: [the provider stdio binding](../drafts/provider-stdio.md) and
[Decision 0019](0019-one-binary.md)

## Context

The first provider stdio draft required separate spawns when one executable
served agent control and direct inference. That made profile identity a process
property even though every OAP envelope already has a required `profile`
discriminator. Makai's original single stdio host exposed both capabilities on
one connection. `oapx` should retain that composability while replacing the
native Makai wire with OAP's two distinct profiles.

## Decision

`oapx serve agent,provider --stdio` is a supported mode: one process, one stdin,
one stdout, and both profiles. The single-profile forms remain supported. The
CLI list is comma-separated; order does not change the wire behavior.

For each complete JSONL envelope, the host reads the top-level `profile`
before decoding the profile-specific `type` or `payload`. It routes the frame
to exactly one profile handler. A request with the agent profile cannot become
a provider request by using a provider-looking `type`, or vice versa. Each
handler validates its own envelope schema and sends its own responses and
events with the same profile value as the request it answers. The router
serializes complete output lines; it does not splice bytes from concurrent
writers.

There is **no connection-level initialize or mandatory global hello**. Agent
control still uses `protocol.initialize.request`; the provider profile still
uses `provider.describe.request`. A caller may use only one profile on a
composed connection. Merely serving two profiles does not require every caller
to implement both, and the host does not send unsolicited events for a profile
the caller never invoked.

Correlation is `(profile, id)`, not `id` alone. Scope and sequence domains are
also independent: agent sessions/runs never share a counter with provider
inferences. Output order across profiles carries no causal guarantee. A caller
that needs an operation in one profile to precede an operation in the other
waits for the first operation's correlated response before sending the second.
One profile's error does not settle or cancel work in the other profile.

An envelope with a recoverable non-empty `id` but absent or unknown `profile`
cannot be attributed to either profile. Until OAP defines a profile-neutral
error envelope, the composed router answers it deterministically with a
correlated agent-control `error.response`, code `invalid_request`, and
`details.feature: "profile"`; it keeps the connection open. This fallback is
for malformed input only and never changes routing of a valid provider
request. Invalid JSON, overlong lines, or an envelope without an addressable
`id` retain the binding's fatal framing behavior.

EOF, signals, and output failure close the **one process**, not an arbitrary
profile. On shutdown each handler follows its existing settlement rule for
accepted work; the shared process exits zero only after both handlers have
completed a clean shutdown. A provider credential grant remains scoped to the
physical connection and its provider profile. Sharing a process does not
implicitly grant credentials to the agent profile or select the co-hosted
provider for any session.

## Consequences

Composition concerns deployment and transport, not a merged vocabulary.
`model-provider-core` remains the direct-inference boundary; agent-control
sessions and runs remain agent-control operations. The agent's use of the
co-hosted provider is an explicit session provisioning/selection decision, as
specified in [Decision 0028](0028-live-model-and-provider-control.md).

A future network binding may carry both profiles, but this decision does not
define the remote HTTP provider binding. The stdio rule is executable without
that follow-up.
