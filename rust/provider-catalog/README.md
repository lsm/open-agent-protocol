# oap-provider-catalog

The Rust port of `go/providercatalog`: the curated model provider catalog in
`providers/catalog.json`, validated against `providers/catalog.schema.json` and
decoded strictly. `Catalog::embedded()` is the checked-in catalog, compiled into
the crate, so a base URL or OAuth origin is read from it and never written as a
literal.

A member the schema does not declare, a member written twice in one object, and
data after the catalog object are refused, as Go's `DecodeStrict` refuses them.
The refusal text is serde's rather than Go's.
