# Decision 0033: Harness Pins Are Data

Status: proposed
Date: 2026-09-24
Protocol: `open-agent-protocol` version `0.1`
Profiles: neither; this record governs how the adapters record which upstream
versions they are proven against, not the wire
Amends: the rule in `CLAUDE.md` that each adapter "is pinned to one upstream
commit or tag"
Gated by: [Decision 0032](0032-go-and-zig-are-peers.md), whose peer rule is why
the pins cannot live in either tree

## Context

Every adapter is the executable form of one upstream version, but nothing
records that version once. A pin is written by hand into its ledger, its corpus
directory name, the corpus `manifest.json` and every `case.json` beside it, Go
constants, Zig literals, the capability revision, the endpoint version and the
real-process gate. Each pin string appears in five to twelve tracked files
outside `research/` and `fixtures/`, and nothing checks that they agree.

They already disagree:

- **Directories name versions they do not hold.**
  `fixtures/adapters/deepseek-harness-47f9438` holds bytes recorded at
  `fb2c4b9`, and its ledger says the code targets a third tag. The claude corpus
  directory says 2.1.263, its provenance says 2.1.263, its host lines carry a
  member only 2.1.280 knows, and its expectations name the 2.1.280 revision.
- **Re-pins follow no convention.** DeepSeek and Makai kept their revision and
  directory across a move; claude renamed its revision and kept its directory.
  `drafts/agent-control-core.md` says a revision identifies the complete
  effective descriptor, so a move that changes the endpoint version must change
  the revision. Only one of the three did.
- **The running version is never checked.** The claude adapter decodes
  `claude_code_version` from `system/init` and never compares it. #232 drove
  claude 2.1.241 through an adapter pinned to 2.1.263 and nothing noticed.
- **A harness can have one version.** There is no way to say an adapter is
  proven against two releases, or to keep an older corpus running as a
  regression floor after a move.
- **Two trees read the pins, and they are peers.** Go and Zig each hard-code
  them. They agree today only because both reproduce the same expectations.

## Decisions

### One catalog, at the root, owned by neither tree

Each harness has one file, `harnesses/<id>.json`, validated by
`harnesses/harness.schema.json`. The catalog sits beside `schema/` and
`fixtures/`, because under Decision 0032 neither implementation owns what both
must agree on. Go embeds it the way it embeds `schema/`; Zig reads it at build
time. It is the only place a pin is written. Everything else is derived from it
or checked against it.

### A harness has versions, and each version has a status

- **`current`**: exactly one per harness. It is what a configuration that names
  no version gets.
- **`supported`**: admitted at runtime, with its own corpus and capability
  revision.
- **`floor`**: its corpus still runs as regression input; the version is not
  admitted at runtime.
- **`retired`**: only its ledger remains.

A version whose evidence was recorded against an older release says so with
`corpus_from`, instead of the directory name implying otherwise.

A version is keyed by upstream's own label. A harness with no releases uses its
short commit.

### What a version records

- the upstream label;
- the endpoint version the adapter reports and its capability revision;
- the runtime versions it admits;
- its ledgers and its corpus directory;
- its components (for claude: the CLI, the TypeScript SDK and the Python SDK);
- its artifacts, **per platform and per kind** — an npm tarball, a binary, a
  source tree — each with its digest and size, because one digest cannot pin a
  harness that ships a different binary per platform;
- the upstream source commits.

### What is derived, and checked offline

A `check` phase in both binaries fails on any of these:

- A capability revision is not unique within its harness, or is unchanged
  across a change of endpoint version.
- A version's revision is not the revision its corpus expectations carry.
- A corpus `manifest.json` does not name its harness and version, or its
  provenance differs from the catalog.
- A listed ledger does not exist, or a catalog digest appears in none of the
  version's ledgers.
- A harness has no `current` version, or more than one.

Go reads the catalog through generated code carrying a `Code generated` header,
which the zero-comment rule already exempts. Zig reads it at build time, so no
generated Zig source is needed. Both trees stop hard-coding pins; the constants
that remain are generated or read, never typed.

The real-process gates take their variable names from the harness and, when
`_SHA256` is unset, default it to the host platform's artifact digest.
`adaptertest.VerifiedBinary` stays the only implementation.

A registry configuration entry may name a version. An empty version means
`current`; a version the catalog does not admit is a load error.

### A version outside the admitted set is refused at open

Where a harness reports its version — claude's
`system/init.claude_code_version`, DeepSeek's `serverInfo`, a `--version`
probe — the adapter compares it against the version's admitted set when the
session opens. Outside that set, the open is refused with a typed error naming
both versions.

An operator who needs to run an unadmitted release anyway opts in on the
registry entry. The session then opens, and the descriptor records the
mismatch as a degradation rather than hiding it.

### Upstream drift is watched, not followed

A scheduled workflow reads release metadata only (npm, PyPI, GitHub releases).
It never downloads an artifact, uses no credential beyond the repository's own
token, and keeps one issue per harness current when upstream moves.

Moving a pin stays a deliberate change with a ledger, never an automatic one.

### A move records a new corpus; it never edits one in place

A new version gets a new corpus directory recorded against it. The old
directory is not renamed and edited: it becomes a `floor` or is removed when the
version retires. A change that rewrites expectations still merges alone,
entirely before or after a port, as Decision 0019 requires.

## Consequences

- `CLAUDE.md`'s "pinned to one upstream commit or tag" becomes "pinned to the
  versions its catalog entry names".
- The corpus directories keep their names. The catalog says what each holds,
  and the mismatches above become visible data rather than surprises.
- The per-case provenance copies in `case.json` go once the catalog carries
  them. That rewrites corpus files, so it is scheduled under 0019's rule, not
  during a port.
- The claude 2.1.280 corpus that is being recorded now is the first version to
  land under these rules.

## What this decision does not admit

- A pin written anywhere except the catalog.
- A corpus directory renamed and edited in place to follow a new release.
- An adapter silently driving a harness version it was never proven against.
- A workflow that downloads upstream artifacts, or re-pins on its own.
