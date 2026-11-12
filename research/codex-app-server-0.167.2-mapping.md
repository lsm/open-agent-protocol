# Codex app-server 0.167.2 mapping ledger

This pin supersedes [0.167.0](codex-app-server-0.167.0-mapping.md); its
unmodified mappings carry forward. The upstream tag `rust-v0.167.2`, published
2026-11-11, resolves to commit
`ce21072c90d32c40af7a55b60b2fb35c84a43a6a`, tree
`ec5084c89f035f9026575ebdbf711a031d429f21`.

## Schema comparison

The generated schema tree digest over the tag's
`codex-rs/app-server-protocol/schema`, by the original ledger's sorted path
and SHA-256 recipe (1085 files), is
`15a28c10689057d59a02fc041bd2841a1b733b7021e8256857ca7627b9cde956` —
byte-identical to the 0.167.0 tree; `diff -r` between the two checkouts'
schema directories reports no differences. The recipe was reproduced on the
0.167.0 checkout first and matches its recorded digest. No
`ClientRequest`, `ServerNotification` or `ServerRequest` shape the adapter
sends, projects or answers changed between the two tags.

The 0.167.1 release notes carry no entries, and 0.167.2's single feature
(incremental tool updates on Google Cloud Vertex AI, behind a config flag)
does not touch the app-server protocol.

Release metadata gives darwin-arm64 archive SHA-256
`7511163b7a0152bcf0e1229092bd2545fdccff64fc8d92ad7c6e01f0ca4681df`
(108630744 bytes) and linux-x64 musl archive SHA-256
`8246e846c550ea21197fcc4067bc270dca895e94ea42ed6a282e6f4e0125b15f`
(122632479 bytes). Archives and binaries were not downloaded or run.

## Corpus

The inherited corpus is carried into
`fixtures/adapters/codex-appserver-0.167.2` (and its `-writes` peer) with
the pinned commit, tree and capability revision moved to 0.167.2; the
0.167.0 corpus directories are removed. Nothing else changed, since the
protocol surface is identical. No real account, credential, model call, or
real-process compatibility check was used. Binary behavior and full upstream
compatibility remain unverified.
