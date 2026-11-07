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

These are staged construction contracts. Complete trace validation follows the
remaining loader and validator integration slices.

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
controls separately from agreeing expectation cases. Corpus trace execution follows
with validator integration.
