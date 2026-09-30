# Extension pack load refusals: what is enforced, and what is not

Companion record for the `--pack` loading path. It states the outcomes a
descriptor can get, codes included, and names every shape that is still skipped
rather than refused. Written to be read next to the code it describes, and to
be corrected when the code changes — not to justify it.

Governing text: `decisions/0004-extension-packs.md` §121-146 (core validity, two
passes, branch pinning) and §165-182 (load refusals). The peer/spec rule that
applies throughout: **Decision 0032** — a Zig/Go divergence is either aligned or
recorded here. Nothing below is asserted to match `goap`; where the two differ,
the difference is written down.

## Refusals, with their codes

A recorded refusal fails the whole load. Branches, members and types are all
emptied, so a pack that is refused also stops the semantic machine from seeing
its declared vocabulary — a partial refusal that kept the good contributions
would leave a pack contributing what it has just refused. The mixed case is
tested.

| code | what is refused |
|---|---|
| `pack_unprefixed_name` | a `capability_keys` entry, `error_codes` entry or declared `envelope_types` type that does not begin with the pack's own `id` + `.` |
| `pack_foreign_prefix` | as above, for a name in a foreign namespace |
| `pack_id_collision` | two loaded packs with equal ids, or whose ids prefix one another |
| `pack_branch_unpinned` | a contributed branch whose resolved schema is an object with no `properties.type.const` |
| `pack_branch_undeclared_type` | a branch whose `const` names a type other than its own declared one |
| *(no code)* | a `schema` ref naming a file the descriptor does not contribute; a pointer that resolves to nothing; a pointer that lands on a non-object; a cited name that climbs out. A cited name with a **leading separator** is *not* refused — it normalises and is accepted; see below |
| *(no code, `InvalidPackDescriptor`)* | a wrong shape for `id`, `version` or `schemas`; a duplicate type within one pack; a `schemas` path that is absolute, climbs out, or does not land beneath the pack root once symlinks are resolved |

The uncoded refusals carry no code **on purpose**. `goap` records no code for
the same two shapes, so inventing `pack_unresolvable_ref` would make oapx louder
than its peer and put a word in the vocabulary `fixtures/manifest.json` does
not have. When oapx prints an uncoded refusal on stderr it renders the label
`unresolved-schema-reference`; `goap` emits no such label, but it still writes a
failure diagnostic — `PackLoadError.Error` formats the pack, code and message
(`go/validation/pack.go:79-80`), `goap` returns it, and `main` writes it to stderr.
So the difference is in the **label**, not in whether output appears: a `goap`
refusal with no code prints no `unresolved-schema-reference`, but it is not silent.
The *code* is identical, the label is not, and the label is the CLI's doing rather
than the loader's.

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
`toSlash(lexicalRelative(schemas[index]))` (`packs.zig:330`), and at citation time
the branch normalises the cited name the same way before lookup (`packs.zig:393-402`),
matching `document.name` (`packs.zig:418`). The two sides are therefore **equal
canonical names after normalisation**, not one value carried from the descriptor —
they are independently derived from different source strings and agree only once
both have been cleaned. Go cleans both sides too, which is why the earlier
divergence — a branch ref built from a cleaned citation against a registry keyed by
the raw `schemas` spelling — produced a branch that every judgement failed to
resolve.

## Accepted by normalization, not skipped

These shapes load and **contribute**, so they are not on the skipped list:

- a cited name **spelled with a leading separator** — `/types.schema.json#/$defs/thing`
  and `//types.schema.json#/$defs/thing` are both **accepted, with no refusal**,
  and a pack whose citation is spelled that way still contributes its branch.
  `cleanRelative` drops the empty separator (`packs.zig:83`), the registration
  loop keys the document by the *normalised* name (`packs.zig:330`), and the branch
  site normalises the cited name the same way (`packs.zig:393-402`), so the cited
  spelling and the registered key are the same string after normalisation and the
  lookup succeeds; the branch is appended and the pack contributes it.

  What is defective is the **spelling's identity**: the descriptor's `schemas`
  lists `types.schema.json`, and the citation is `/types.schema.json`, which is not
  a spelling that appears anywhere in the descriptor. A reader auditing the pack by
  its own text cannot find the resource the branch uses, and nothing in the load
  says the citation was rewritten. Whether a citation must *be* one of the
  descriptor's own spellings is a **registered-resource identity question against
  Decision 0004**, and it is not answered here. Measured on `7dd74dd30d`:
  `valid:true`, exit 0, no refusal. (A *climbing* name is different: `..` is refused
  uncoded, as measured above.)

## Skipped, not refused

Named so the disclosure shrinks with the code rather than lagging it. "Skipped"
means the load **succeeds** but the shape contributes **no branch, member or type**
the loader can see — it is neither refused nor used:

- an `envelope_types` entry with **no `schema` field at all** — the branch-schema
  check is skipped, so the entry contributes no branch, and the declared type
  reaches the semantic machine anyway. `goap` refuses it. Whether absence should be
  refused is a **contract question about Decision 0004 §140** and is with the owner;
  it is deliberately not answered here, because `fixtures/packs/*/pack.json` treats
  `type` as the only required field.
- a **wrong-shaped but present** `schema` is a separate matter from absence and
  has its own repair.

## Limits of this record

- The lexical climb check is not independently observable on POSIX; the
  absolute-entry and subdirectory cases are likewise unobservable there.
- `toSlash` rewriting a Windows separator is not observable on POSIX, so the
  URI assertions in the tests pin an invariant rather than prove a fix on this
  platform.
- `checkAllAllocationFailures` is deliberately not applied to `gather`; the
  freeing test in `validator.zig` covers `Validator.init`, not the loader.
- "Skipped" above means the load succeeds and the shape contributes nothing the
  loader can see. It does not mean the trace is judged correctly.
