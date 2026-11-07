# Staged pack-loader construction

`PackLoader::read` constructs the declaration stage from the first of six loader slices. It checks
descriptor shape, declared namespaces, prefix freedom, envelope roles, reply
targets, refusal declarations and gates. Payload-member declarations require
unique names on core targets, preserve core members and name their gate.
`descriptors` exposes immutable views of the retained roots and descriptor values.

`read_resources` consumes the declaration stage and returns `PackResources` after
reading schema files and resolving fixture-manifest paths beneath their pack
roots, including symlink containment. Dependencies must be present at their exact
versions. Schema references resolve only to the owning pack, the core bundle and
declared dependencies. Schema identifiers stay within their own pack namespace.
`documents` exposes immutable schema values indexed by resource URI,
and `manifests` exposes the contained manifest paths indexed by the pack base URI.

These methods expose construction stages. `PackLoader::load` completes
construction, and the Validator constructors below execute traces.

`PackResources::compile` pins every contributed branch to its declared type,
prepares the core and pack schema registry, and returns `CompiledPacks`. Its
`schema` accessor provides the composed envelope schema, `member_schemas` provides
the separately compiled member schemas, and `resources` retains the construction
data. For a core envelope carrying declared members, callers validate its core
projection and validate the removed values against their member schemas.

`CompiledPacks::check_corpus_entries` performs the fifth staged operation. It
reads declared manifests into typed entries, checks version 1, positive and
negative expectations, provider profiles and scopes, and the separate agent,
provider and load-error code vocabularies. Optional null fields retain their
zero/default meaning. Unknown manifest fields remain accepted.

This operation checks declared kinds, phases, verdicts and code vocabularies.
The final constructor adds the remaining corpus policy checks in the sixth slice.

- Agent mode and provenance validation includes the mode vocabulary and empty
  provenance strings.
- Nested pack declarations and empty agent units are refused by full construction.
  Unit ownership requires a pack's own loaded extension term.
- Identity and path policy includes required IDs and paths, contained paths,
  duplicate IDs and duplicate paths.
- Coverage policy requires well-formed negative gate/honour claims and complete
  coverage for every declared capability, including a required manifest.

The staged operation can accept an entry the complete Go loader refuses for any
of these deferred policies. The public verification journey records representative
controls separately from agreeing expectation cases. Use `Validator::validate`
to execute a corpus trace.

## Complete construction

`PackLoader::load` performs declaration, resource, reference, compilation and
corpus checks and returns `CompiledPacks` only after they succeed. Agent corpus
entries require IDs, contained paths, units and consistent mode/provenance values.
They cannot load additional packs and must claim their own loaded extension term.
Duplicate IDs and paths are refused. Provider entries retain their profile's
manifest rules, including normalized path uniqueness.

Every declared capability needs a manifest with a negative gate fixture and a
negative honour fixture under the owning extension term. Construction validates
the corpus declarations. `Validator::validate` executes trace data after construction.
The earlier `check_corpus_entries` method retains its narrower staged contract.

## Parsed-envelope schema validation

`CompiledPacks::validate_schema(value, fixture)` checks one parsed JSON envelope
and returns its primary schema diagnostic on failure. It projects declared
members out of the core payload, validates the composed envelope, then validates
the removed member values. The caller's value remains intact. Diagnostics refer
to the original payload with escaped JSON Pointer tokens and deterministic
pointer order. Auth-reply diagnostics retain sensitive-answer redaction.

Packed envelope errors select the branch declared for that type. Four public
controls record Go's existing selection of unrelated core-branch errors while
Rust points at the declared packed payload. Their schema verdicts agree.
This operation covers parsed-envelope schemas.

## Native strict trace validation

`Validator::with_packs(directories)` loads the complete pack set and returns a
strict trace validator. Construction returns `PackLoadError` for refused packs.
`validate` uses the composed schema, declared request/response/event roles,
request-time advertisement captures and exact gate keys. Successful responses
must be declared for their request. An event's `replies_to` field has no effect
on response selection. A request with no declared success type can be refused.

Declared packed run events share core run sequencing and terminal checks.
Packed scope falls back from payload IDs to envelope IDs, and declared members
retain core schema and semantic checks. Every validation call starts fresh trace
state while sharing immutable compiled packs. `Validator::new` retains ordinary
core validation.

## Tolerant trace validation

`Validator::tolerant()` constructs a tolerant core validator, and
`Validator::tolerant_with_packs(directories)` adds the fully loaded pack set.
Semantic recognition uses the protocol vocabulary, independently of schema
declarations. Schema-only task types retain generic tolerant scope and run
checks while their task semantics remain staged under Decision 0069.
Pack construction enforces the complete strict loader contract before compiling
tolerant schemas. Trace validation accepts additive object members, future string
enum values and future variants in discriminator unions whose branches are inline
or referenced within the same schema document. Known constants, required fields,
numeric enums and conditional tests retain their constraints. Unknown envelope
types still require the common envelope fields.

Unknown run statuses and admission values are opaque to rules requiring known
vocabulary. Known values still constrain each other, and known status transitions
resume their table checks. An opaque admission still requires a terminal event.
Unknown support levels fail capability gates. Unknown envelope requests, replies
and sequenced events retain correlation, scope, sequence and terminal checks.
String scope IDs in extension payloads are read independently.
