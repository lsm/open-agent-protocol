# Extension packs

Decision 0004, which makes good 0003's extension seam: the load refusals a pack set can draw, and the gate diagnostics packed content draws on the wire. A load refusal is not a diagnostic (0004 L165–173): it comes from the loader before any trace is read, and `load-invalid` fixtures judge it. Format: [README](README.md).

## Load refusals

### [0004] The unprefixed namespace is the spec's, in its entirety

Source: [`decisions/0004-extension-packs.md`](../../decisions/0004-extension-packs.md#the-unprefixed-namespace-is-the-specs-in-its-entirety).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0004#the-unprefixed-namespace-is-the-specs-in-its-entirety/1` | Every name a pack declares MUST carry a reverse-DNS prefix. | L57–67 | `pack_unprefixed_name` | `pack.go:LoadPacks` | `packs.zig` | — |
| `0004#the-unprefixed-namespace-is-the-specs-in-its-entirety/2` | Every name a pack declares MUST begin with that pack's own `id` followed by a dot. | L65–67 | `pack_foreign_prefix` | `pack.go:LoadPacks` | `packs.zig` | — |

### [0004] The loaded set is prefix-free

Source: [`decisions/0004-extension-packs.md`](../../decisions/0004-extension-packs.md#the-loaded-set-is-prefix-free).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0004#the-loaded-set-is-prefix-free/1` | No loaded pack's `id` MAY equal, or be a dot-prefix of, another loaded pack's. | L75–82 | `pack_id_collision` | `pack.go:LoadPacks` | `packs.zig` | — |

### [0004] A pack declares what it defines

Source: [`decisions/0004-extension-packs.md`](../../decisions/0004-extension-packs.md#a-pack-declares-what-it-defines).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0004#a-pack-declares-what-it-defines/1` | A pack MUST declare each envelope type once. | L86–90 (drafted here: a declaration is a definition, and one name has one) | `pack_type_duplicate` | `pack.go:LoadPacks` | `packs.zig` | — |
| `0004#a-pack-declares-what-it-defines/2` | Every declared request, event and core-payload member MUST appear in `gates` or be declared ungated. | L94–97 | `pack_ungated_type` | `pack.go:LoadPacks` | `packs.zig` | — |
| `0004#a-pack-declares-what-it-defines/3` | A declared `response` MUST NOT have a gate of its own. | L97–100 | `pack_response_gated` | `pack.go:LoadPacks` | `packs.zig` | — |
| `0004#a-pack-declares-what-it-defines/4` | Every declared envelope type MUST declare its `role`: `request`, `response` or `event`. | L101–104 | `pack_role_undeclared` | `pack.go:LoadPacks` | `packs.zig` | — |
| `0004#a-pack-declares-what-it-defines/5` | A declared `response` MUST name in `replies_to` a request the same pack declares. | L97–100, L107–108 (drafted here: a response takes its gate from its request's, which only a request the pack declares carries in its `gates`) | `pack_reply_target_unknown` | `pack.go:LoadPacks` | `packs.zig` | — |
| `0004#a-pack-declares-what-it-defines/6` | A declared request MUST be named by at most one declared `response`. | L107–108 (drafted here: correlation names one answer type) | `pack_reply_target_ambiguous` | `pack.go:LoadPacks` | `packs.zig` | — |
| `0004#a-pack-declares-what-it-defines/7` | `refusals` MUST be keyed by requests the pack declares and list error codes it declares. | L109–115 (drafted here for the keying) | `pack_refusal_undeclared` | `pack.go:LoadPacks` | `packs.zig` | — |

### [0004] A pack adds vocabulary; it does not amend the protocol

Source: [`decisions/0004-extension-packs.md`](../../decisions/0004-extension-packs.md#a-pack-adds-vocabulary-it-does-not-amend-the-protocol).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0004#a-pack-adds-vocabulary-it-does-not-amend-the-protocol/1` | A declared payload member MUST target a core payload type. | L88–89, L123–127 | `pack_member_target_unknown` | `pack.go:LoadPacks` | `packs.zig` | — |
| `0004#a-pack-adds-vocabulary-it-does-not-amend-the-protocol/2` | A pack MUST declare each payload member at most once. | L88–89 (drafted here, as for envelope types) | `pack_member_duplicate` | `pack.go:LoadPacks` | `packs.zig` | — |
| `0004#a-pack-adds-vocabulary-it-does-not-amend-the-protocol/3` | A pack MUST NOT declare a member the core payload already defines. | L134–138 | `pack_restates_core_member` | `pack.go:LoadPacks` | `packs.zig` | — |
| `0004#a-pack-adds-vocabulary-it-does-not-amend-the-protocol/4` | Every schema branch a pack contributes MUST be for an envelope type it declares. | L140–146 | `pack_branch_undeclared_type` | `pack.go:LoadPacks` | `packs.zig` | — |
| `0004#a-pack-adds-vocabulary-it-does-not-amend-the-protocol/5` | Every contributed branch MUST pin `type` to a `const` naming its own declared type. | L140–146 | `pack_branch_unpinned` | `pack.go:LoadPacks` | `packs.zig` | — |

### [0004] A pack is a document someone else wrote

Source: [`decisions/0004-extension-packs.md`](../../decisions/0004-extension-packs.md#a-pack-is-a-document-someone-else-wrote).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0004#a-pack-is-a-document-someone-else-wrote/1` | A pack's schemas MUST resolve references only to the core bundle, its own documents and the packs it names in `depends_on`. | L150–152 | `pack_external_ref` | `pack.go:LoadPacks` | — | — |
| `0004#a-pack-is-a-document-someone-else-wrote/2` | Each `depends_on` entry MUST be satisfied by a loaded pack of that exact id and version. | L152–155 | `pack_dependency_missing` | `pack.go:LoadPacks` | — | — |
| `0004#a-pack-is-a-document-someone-else-wrote/3` | Every `schemas` entry, and the pack's fixture manifest, MUST resolve beneath the pack root after symlinks are followed. | L157–163 | `pack_schema_path_escape` | `pack.go:LoadPacks` | — | — |

### [0004] A pack's conformance claim is its own

Source: [`decisions/0004-extension-packs.md`](../../decisions/0004-extension-packs.md#a-packs-conformance-claim-is-its-own).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0004#a-packs-conformance-claim-is-its-own/1` | A pack's fixture MUST NOT claim a core unit. | L192–195 | `pack_fixture_claims_core_unit` | `pack.go:LoadPacks` | — | — |
| `0004#a-packs-conformance-claim-is-its-own/2` | An `ext:` claim MUST name a pack of that exact id and version that is loaded. | L192–194 | `ext_claim_without_pack` | `pack.go:LoadPacks` | — | — |

## Gate diagnostics

These three are rows of the same `### A pack declares what it defines` rule, numbered after its load refusals.

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0004#a-pack-declares-what-it-defines/8` | An event, or a success response, carrying a packed type or member MUST NOT appear unless its gate key is advertised above `unavailable`. | L101–107 | `unavailable_capability` | `packstate.go:packEnvelope` | `packEnvelope` | — |
| `0004#a-pack-declares-what-it-defines/9` | A packed request under an unadvertised gate key MUST be refused with the typed unsupported-feature error naming that key. | L103–107 | `unavailable_capability` | `packstate.go:settleRefusal` | `packEnvelope` | — |
| `0004#a-pack-declares-what-it-defines/10` | An endpoint advertising a pack's key MUST NOT refuse a schema-valid packed request under a code that request's `refusals` does not list. | L109–115 | `unhonoured_capability` | `packstate.go:settleRefusal` | `packEnvelope` | — |
