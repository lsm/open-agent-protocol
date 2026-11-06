# Staged pack-loader construction

`PackLoader::read` constructs the first of three pack-loading stages. It checks
descriptor shape, capability/error/type namespaces, prefix freedom, envelope
roles, reply targets, declared refusals and type gates. `descriptors` exposes
immutable views of the retained roots and descriptor values.

A successful constructor establishes that declaration-stage contract. Schema
resources, payload members, dependencies, references, branch compilation and
fixture corpora are covered by the following loader stages. Trace validation
becomes available once the complete loader is connected.
