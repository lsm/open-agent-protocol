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
