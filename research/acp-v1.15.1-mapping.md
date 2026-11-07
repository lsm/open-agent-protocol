# ACP v1.15.1 mapping ledger

This pin builds on the [v1.12.0 ledger](acp-v1.12.0-mapping.md) and its
predecessors. ACP wire version remains 1; ACP v2 is a separate protocol and is
not included in this adapter.

## Source and scope

- ACP release `v1.15.1` and schema release `schema-v1.29.1` both resolve to
  commit `0fc18323a9fdad4e5b4a20ec2f1645dde843fd9b`.
- Specification tree SHA-256:
  `c18b3595e4a7a431d0b197416afead0374eb83107e58988121f6288700a142d3`.
- Schema tree SHA-256:
  `478575a93ca6b7c38bdbfa0f379199138dedb48439bbfcfbfda962495ac70eb7`.
- Hash recipes are those in the [v1.7.0 ledger](acp-v1.7.0-mapping.md).

The stable v1 generated schema was compared with schema-v1.26.0. Excluding
prose, titles, examples and generator-specific `x-` hints, its definitions
have no structural differences. The new hints mark some lists as defaulting
on null. Stable schema prose clarifies currency units and deserialization;
unstable schemas add proposals including status, archive, title and proxy
methods. Those proposals are not stable v1 adapter capabilities. The stable
v1 metadata registry is unchanged. The v2 schema was not used as v1 evidence.

## Evidence boundary

The new corpus carries forward the prior corpus unchanged at the native wire
level, with updated pin metadata and capability revision; it is not a fresh
recording from an agent. The synthetic corpus covers the adapter mapping, not
interoperability with every ACP client or agent. In particular Devin Desktop is
a proprietary ACP client target, and its behavior at this pin has not been
verified against a live product. The Devin evidence limitations and mapped
profile in the base ledger continue to apply.

The catalog retains cagent v1.150.0 as the separately tested agent pin; its
previous process-gate evidence is recorded in the v1.12.0 ledger. No new
cagent or Devin live test is claimed by this pin update.

Rust ACP corpus replay remains an existing documented gap in
`rust/adapter/acp/README.md`; the Go and Zig corpus checks do not establish
Rust corpus parity.

## Staged Rust tool and permission projection

The Rust `-rust-reload` stage projects observed native tool calls and sparse
patches through the normal session submit stream. Tool identity is session-wide,
terminal events synthesize a missing start, and unfinished children settle in
creation order before the parent. The synthetic tools journey compares public
Go/Rust results and native host frames, then validates actual Rust traces with
all three validators. It normalizes wall clocks and the declared staged revision.
Byte-for-byte corpus replay retains its final approved ACP slice.

The degraded tool rows describe observed native execution. Permission
publication/resolution uses the native support described below. Native host cancellation and live config are described below. Reload is described below; opening configuration retains its following slice. Closing or abandoning an active session
retains its existing bounded prefix and owned process cleanup contract.

## Typed Rust permission projection

The public TextRun projection now handles typed permission publication, answer
validation, native reply preparation and correlated commit. Parent transitions
wait for gate settlements, then settle tools and the parent in order. The
standalone native bridge consumer compares fixed-clock/id event JSON and
native host frames with Go, with all three validators checking actual Rust
events under a synthetic host prefix. This is component evidence. Normal Rust
Session integration uses the owned controller described below. Full corpus
replay retains its separate approved slice.

## Owned Rust permission controller

The standalone Permissions component binds requests to their actual bridge,
serializes response writes with correlated commits and parent settlement.
Ordinary notification projection runs inline under the run lock without waiting
for native writes. Parent results and settlement use bindings-before-run ordering,
serializing accepted cancel intent with native confirmation. Native I/O is owned.
The native consumer
compares fourteen Go/Rust event and parsed-wire cases, including trailing
notifications. Blocked selected and settlement replies, queued cancellation
and bridge close use synthetic native children and return OS threads to their
starting inventory after child reap. Normal Session permission integration is
described below. The byte-for-byte corpus remains a later gate.

## Normal Rust Session permission integration

Normal Session publication, resolution, admission and close now use the owned
controller. The descriptor exposes native action.permissions and interactive
gates under -rust-reload. Immediate control answers serialize behind
publication, and state reads reflect the current projection. Live permission routes are pruned
at commit or settlement. A bounded cache of 64 resolved ids preserves recent
late-answer refusals across subsequent admissions. Idle native asks receive
cancellation or malformed-request errors. Accepted native replies consume
their bindings before a local commit that can fail after close.

The normal public Go/Rust consumer comparison retains raw outputs and host
frames. It normalizes wall-clock values and the declared staged revision for
event comparison. Actual Rust traces use the actual Rust descriptor/admission
and pass all three validators. Close/drop proof uses native quiet peers and
OS thread inventories. Full byte-for-byte corpus replay remains a later gate.

## Rust native cancellation

The -rust-reload stage accepts run-targeted native session/cancel intent,
preserves cancelling through streamed events and waits for a cancelled stop
or remote -32800 confirmation. Completion or another error may win. Pending
permissions settle with native cancellation and correlated local resolutions;
late asks receive cancellation without another OAP permission prompt. Public
Go/Rust events and parsed host frames are compared after clock/revision
normalization, and all three validators judge actual Rust events with the
accepted host exchange. A controlled clock pauses accepted-write commit to
prove confirmation waits. Quiet peers prove queued control drop/close reap
and join. Run replay/resume, opening config, full byte corpus and C8 remain later gates.

## Rust live config projection

The normal Session now applies an agent's `config_option_update` inline in its
ordered dispatcher, outside and during a run. The full list replaces the model
catalog and option used by later switches. Recorded model/reasoning follow the
existing mapping in the v1.11.0 ledger: model placeholders preserve the previous
value, missing/unreadable reasoning clears it, and the state timestamp changes
only when those recorded settings change. No session or run event is invented.
Malformed active updates retain `acp_invalid_update`; malformed idle or foreign
session updates leave recorded settings alone.

The synthetic public Go/Rust comparison retains both directions of native wire,
snapshots and run events, normalizing only clocks and the staged revision. All
three validators judge wrapped Rust exchanges. This adds no capability support
level. The current stage uses `-rust-reload` for native reopen, described below.
Confirmed opening config/reasoning remains the next slice, followed by full ACP
byte corpus replay. No served-parity or live-agent claim is made.

## Rust capability-gated reload

The -rust-reload stage advertises native session.open.reopen. It follows the
existing native mapping: loadSession=true selects session/load first, otherwise
a sessionCapabilities.resume object selects session/resume. It refuses absent
bindings and unsupported agents, preserves omitted/null bound ids, and refuses
changed ids, malformed results or native failures. The owned collector drains
history and reverse calls through the bounded queue before the reload result.
It creates no OAP history and reports recovery plus returned settings. A fresh
prompt does not attribute reloaded cumulative usage to the new run.

Public synthetic Go/Rust comparisons retain both directions of native wire,
state/catalog/event output and full refusal diagnostics. Successful results
normalize only clocks/revision; refusals compare feature/reason, since parser
diagnostic wording differs. All three validators judge successful Rust traces.
Quiet and partially written reverse replies prove child reap and joined
workers on cancellation/deadline, before the public consumer exits. Scoped
call-worker ownership preserves the RPC deadline when reverse-write teardown
also produces a broken pipe. Opening config/reasoning and byte corpus replay
remain the next approved slices. This is not a C8 served-parity hand-off.
