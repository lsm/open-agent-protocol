# Extension pack load refusals: what is enforced, and what is not

Companion record for the `--pack` loading path. It states the outcomes a
descriptor can get, codes included, and names every shape that is still skipped
rather than refused. Written to be read next to the code it describes, and to
be corrected when the code changes — not to justify it.

Governing text: `decisions/0004-extension-packs.md` §130-155 (core validity, two
passes, branch pinning) and §174-190 (load refusals). The peer/spec rule that
applies throughout: **Decision 0032** — a Zig/Go divergence is either aligned or
recorded here. Nothing below is asserted to match `goap`; where the two differ,
the difference is written down.

## Refusals, with their codes

A recorded refusal fails the whole load. Branches, members and types are all
emptied, so a pack that is refused also stops the semantic machine from seeing
its declared vocabulary. The loader runs goap's phases in goap's order and stops
at the first phase that refuses: prefix-freedom, declarations, schema paths,
dependencies, references, branches, the pack's own corpus. The per-code status, with the decision text
behind each code, is in
[`validator-rules/0004-packs.md`](validator-rules/0004-packs.md).

| code | what is refused |
|---|---|
| `pack_id_collision` | two loaded packs with **distinct roots** and equal ids, or ids that prefix one another; the same directory supplied twice is skipped before the check, so it is not a collision |
| `pack_unprefixed_name` | a capability key, error code, declared envelope type, payload member name or gate capability that does not begin with the pack's own `id` + `.`, and is in a core namespace or too short to be in any |
| `pack_foreign_prefix` | as above, for a name in another pack's namespace |
| `pack_type_duplicate` | one envelope type declared twice in one pack |
| `pack_role_undeclared` | an envelope type whose `role` is not `request`, `response` or `event` |
| `pack_reply_target_unknown` | a response whose `replies_to` names no declared request of the pack |
| `pack_reply_target_ambiguous` | two responses answering one request |
| `pack_refusal_undeclared` | a type's `refusals` entry the pack never declared in `error_codes` |
| `pack_response_gated` | a response carrying a gate of its own |
| `pack_ungated_type` | a request, event or payload member with no gate and no `ungated: true` |
| `pack_member_duplicate` | two payload members with the same `payload_type` and `member` |
| `pack_member_target_unknown` | a payload member targeting a type that is not a core envelope type, or targeting `capabilities.updated` under a gate |
| `pack_restates_core_member` | a payload member restating a member the core payload already defines |
| `pack_schema_path_escape` | a `schemas` entry or `fixtures` manifest that is empty, absolute, climbs out, does not exist, or resolves outside the pack root once symlinks are followed |
| `pack_dependency_missing` | a `depends_on` entry naming a pack not loaded at that exact version |
| `pack_external_ref` | a `$ref` or `$dynamicRef` resolving outside the pack's own documents and payload members, the core bundle and its declared dependencies; a `$id` outside the pack's own base; a reference that is not a URI |
| `pack_branch_unpinned` | a contributed branch whose resolved schema is an object with no `properties.type.const` |
| `pack_branch_undeclared_type` | a branch whose `const` names a type other than its own declared one |
| *(no code)* | an envelope type with no string `schema`; a gate naming neither a type nor a payload member, both or neither of `capability` and `ungated`, a capability inside the namespace but absent from `capability_keys`, a type the pack does not declare, or a key already gated; a payload member with no `schema`; a `schema` ref naming a file the descriptor does not contribute, a pointer that resolves to nothing or lands on a non-object, or a cited name that climbs out |
| *(no code, `InvalidPackDescriptor`)* | a descriptor that is not an object; a non-string `id` or `version`; a non-array `schemas`; a non-string item in `schemas`, `capability_keys` or `error_codes` |

The last phase reads a pack's fixture manifest, as goap's corpus check does:

| code | what is refused |
|---|---|
| `pack_fixture_claims_core_unit` | a fixture in a pack's own manifest claiming a core unit, such as `core` |
| `ext_claim_without_pack` | a fixture claiming an `ext:<id>/<version>` unit no loaded pack carries |
| *(no code)* | a pack that declares capability keys and ships no manifest; a manifest that cannot be read or decoded; a fixture with no units; a fixture claiming another loaded pack's unit; a capability key with no `semantic-invalid` fixture covering its `gate` and its `honour` aspects under the pack's own unit |

Of goap's manifest checks, the loader ports only these. The rest of
`LoadManifestWith` (fixture kinds, phases, codes, coverage aspects, modes, named
packs, the fixture files themselves), and the validation of the pack's corpus
traces against the loaded packs, are not run, and that is a Zig port gap of the
same kind as the descriptor schema below.

The uncoded refusals carry no code **on purpose**. goap records no code for the
same shapes, so inventing one would make oapx louder than its peer and put a word
in the vocabulary `fixtures/manifest.json` does not have. When oapx prints an
uncoded refusal on stderr it renders the label `unresolved-schema-reference`;
goap prints its refusal message instead. The *code* is identical, the label is
not, and the label is the CLI's doing rather than the loader's.

## The descriptor schema is not applied

goap validates every `pack.json` against `schema/v0.1/pack.schema.json` before
any coded check, and refuses a descriptor the schema rejects, uncoded. oapx does
not apply that schema. The schema is part of the normative bundle and Decision
0004 makes the descriptor the declaration the loader checks (L84–90), so this is
a **Zig port gap**, not a difference of rule. Until it is closed, a descriptor the
schema rejects reaches oapx's own checks and meets one of three fates:

- **Skipped.** The shape reads as absent and the load can succeed: a non-array
  `payload_members`, `envelope_types`, `gates`, `depends_on`, `capability_keys`
  or `error_codes`; a non-array `refusals` on a type; a payload member whose
  `schema` is present but not an object, which contributes no member; an unknown
  property anywhere, such as a misspelled `envelop_types`, whose declarations are
  silently dropped; a gate whose `capability` is not a string, read as absent, so
  a gate that also says `ungated: true` loads as ungated; repeated values where the
  schema demands `uniqueItems`; a string `id` that is not reverse-DNS, an empty
  `version`, and an absent or empty `schemas`.
- **Refused uncoded.** The shape reaches a check that records no code, as goap's
  schema failure records none: a non-string `schema` on a type, or a gate with no
  `capability` whose `ungated` is a string, which leaves it naming neither mode.
- **Refused with a code goap does not give.** The shape reaches a coded check
  before anything refuses it uncoded: a type with no string `type` and a payload
  member with no string `member` are `pack_unprefixed_name`; a payload member with
  no string `payload_type` is `pack_member_target_unknown`; a non-string or
  unrecognised `role` is `pack_role_undeclared`; a non-string `replies_to` is
  `pack_reply_target_unknown`; a non-string `refusals` item is
  `pack_refusal_undeclared`. No `load-invalid` fixture exercises any of these, so
  the two trees disagree on no fixture today; closing the port gap removes the
  disagreement before one does.

## Pointer contract

A branch's `schema` ref is resolved at load against the parsed value of the very
bytes that were registered, and the branch is appended only if the ref resolves
and the definition it names pins `type` to a `const` equal to that branch's own
declared type. Two rules, both taken from `goap`'s loader rather than invented:

- **Objects only.** `resolvePointer` requires a map at every step and returns nil
  on an array, so a branch citation never traverses one. Measured: `#/$defs/arr/0`
  with `arr` a JSON array is refused here and there. The generic RFC 6901 resolver
  in `jsonschema.zig` **keeps** array traversal, because that is what RFC 6901
  says; the object-only rule is scoped to the branch-load contract, which is the
  only place a peer-parity claim is made.
- **A non-object node is uncoded.** `resolveBranch` rejects a non-map node
  *before* the pin check, so `pack_branch_unpinned` is reserved for an object that
  lacks the const.

A ref with **no fragment** takes the whole document as the branch. Measured on
`origin/main` with two documents that differ only in shape:

| document | outcome |
|---|---|
| the pinned object is the document **root** | **loaded**, trace judged `valid:true` |
| the pinned object sits under `$defs` | refused **`pack_branch_unpinned`** |

Measured on fetched `origin/main` `844a2228f`, six probes through the built
binary; the same outcomes were observed on the earlier base `23b642e9c`, so
nothing here depends on which of those two the measurement came from.

**Re-measured on fetched `origin/main` `7dd74dd30d`**, and every row below holds
on that base as it held on `844a2228f`. The earlier base was labelled old-head
evidence because `main` had moved and no slot had been granted; that is no longer
the case, and this file now states current behaviour rather than a superseded
measurement. The leading-separator row was re-checked first, since a citation that
is accepted and *contributing* a branch is the sharpest claim here.

Raw results, exit status captured before any filtering, six invocations of the
built binary:

| probe | exit | refusal printed | verdict |
|---|---|---|---|
| no-fragment, pinned object is the document root | 0 | none | `valid:true` |
| no-fragment, pinned object under `$defs` | 1 | `pack_branch_unpinned` | no verdict |
| `../types.schema.json#/$defs/thing` | 1 | empty code, rendered `unresolved-schema-reference` | no verdict |
| `types.schema.json#/$defs/thing` (control) | 0 | none | `valid:true` |
| `/types.schema.json#/$defs/thing` | 0 | none | `valid:true` |
| `//types.schema.json#/$defs/thing` | 0 | none | `valid:true` |

So a no-fragment ref is **not** skipped and not uniformly refused: it is judged
against the document root, and it is accepted exactly when that root carries the
pin. This is a behaviour change from before the ref check existed, where such a
ref was skipped; the earlier wording in this file said "skipped" and was wrong.

A citation whose cleaned name **climbs out** — `../types.schema.json#/$defs/thing` —
is refused **uncoded** (rendered `unresolved-schema-reference` on stderr) and
yields no verdict. It is not a successful skip. Measured on `origin/main`.

Both rows above are our own loader's outcomes. Neither is taken from `goap`, which
is consulted only for where a peer-parity claim is made and is recorded as such.

## The registry key and the cited name

The key at registration is the normalised schema name
`toSlash` of the path `containedPath` cleaned with `cleanRelative`, and at
citation time the branch normalises the cited name the same way before comparing
it to `document.name`. The
two sides are therefore **equal
canonical names after normalisation**, not one value carried from the descriptor —
they are independently derived from different source strings and agree only once
both have been cleaned. Go cleans both sides too, which is why the earlier
divergence — a branch ref built from a cleaned citation against a registry keyed by
the raw `schemas` spelling — produced a branch that every judgement failed to
resolve.

## Accepted by normalization, not skipped

These shapes load and **contribute**, so they are not on the skipped list:

- a cited name **spelled with a leading separator** — `/types.schema.json#/$defs/thing`
  and `//types.schema.json#/$defs/thing` are both **accepted, with no refusal**:
  `cleanRelative` splits on `std.Io.Dir.path.isSep`, which matches
  both `/` and `\` on Windows and `/` elsewhere, so the empty part before the
  separator is dropped on every platform and a pack whose citation is spelled that
  way contributes its branch.
  `cleanRelative` drops the empty separator, the registration
  loop keys the document by the *normalised* name, and the
  branch site normalises the cited name the same way, so the cited
  spelling and the registered key are the same string after normalisation and the
  lookup succeeds; the branch is appended and the pack contributes it.

  **This is a recorded divergence from `goap`, not a shared outcome.** `goap`
  *refuses* the same citation, uncoded, and the whole load fails. Registration
  there keys the document by `p.Base + filepath.ToSlash(filepath.Clean(rel))`
  (`go/validation/pack.go:736`), and `filepath.Clean("types.schema.json")` keeps no
  leading slash, so the registered key is `…/types.schema.json`. The lookup builds
  the uri with `p.Base + path.Clean(filepath.ToSlash(file))`
  (`go/validation/pack.go:996`), and `path.Clean("/types.schema.json")` **retains**
  the leading slash, so the key misses and `resolveBranch` returns the uncoded
  "schema is not one the descriptor contributes" refusal
  (`go/validation/pack.go:997-1000`), which `LoadPacks` turns into a failed load
  (`go/validation/pack.go:204-205`). oapx accepts where `goap` refuses; this
  asymmetry is the divergence Decision 0032 requires recording here, and it is
  recorded rather than aligned in this cut.

  What is defective on the oapx side is the **spelling's identity**: the
  descriptor's `schemas` lists `types.schema.json`, and the citation is
  `/types.schema.json`, which is not a spelling that appears anywhere in the
  descriptor. A reader auditing the pack by its own text cannot find the resource
  the branch uses, and nothing in the load says the citation was rewritten. Whether
  a citation must *be* one of the descriptor's own spellings is a
  **registered-resource identity question against Decision 0004**, and it is not
  answered here. oapx measured on `7dd74dd30d`: `valid:true`, exit 0, no refusal.
  (A *climbing* name is different: `..` is refused uncoded on both loaders, as
  measured above.)

## Limits of this record

- The lexical climb check is not independently observable on POSIX; the
  absolute-entry and subdirectory cases are likewise unobservable there.
- `toSlash` rewriting a Windows separator is not observable on POSIX, so the
  URI assertions in the tests pin an invariant rather than prove a fix on this
  platform.
- `checkAllAllocationFailures` is deliberately not applied to `gather`; the
  freeing test in `validator.zig` covers `Validator.init`, not the loader.
