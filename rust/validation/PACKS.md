# Staged pack-loader construction

`PackLoader::read` constructs the first of three pack-loading stages. It checks
descriptor shape, declared namespaces, prefix freedom, envelope roles, reply
targets, refusal declarations and gates. Payload-member declarations require
unique names on core targets, preserve core members and name their gate.
`descriptors` exposes immutable views of the retained roots and descriptor values.

`read_resources` consumes the declaration stage and returns `PackResources` after
reading schema files and resolving fixture-manifest paths beneath their pack
roots, including symlink containment. Dependencies must be present at their exact
versions. `documents` exposes immutable schema values indexed by resource URI,
and `manifests` exposes the contained manifest paths indexed by the pack base URI.

These are staged construction contracts. Reference ownership, branch compilation
and fixture corpus checks belong to the following loader stage. Trace validation
becomes available once the complete loader is connected.
