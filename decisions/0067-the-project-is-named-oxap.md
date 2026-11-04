# Decision 0067: The Project Is Named OXAP

Status: accepted 2026-11-03 (implementation follows separately)
Date: 2026-11-03
Protocol: `open-agent-protocol` version `0.1`
Profile: all existing profiles
Amends: project naming; no protocol semantics

## Context

The project is named OXAP. Existing documentation calls it Open Agent Protocol
or OAP, and the released CLI is `oapx`. A project rename must distinguish the
names people use from identifiers that existing peers, saved state and pinned
harness traces already understand.

This decision records the name and the compatibility boundaries before any
executable, configuration, repository or package migration. It changes no
runtime behavior. README commands continue to use the released `oapx` until
the new executable is available.

## Decision

### Project and CLI names

Use **OXAP** as the project name. The preferred CLI name will be `oxap`.
`oapx` remains a deprecated compatibility alias for at least two minor versions
after the first release providing `oxap`; it must invoke the same implementation
with the same arguments and exit behavior. Any deprecation notice goes to
stderr, never into protocol stdout.

### v0.1 wire identities retain their spelling

The project rename does not introduce alternate spellings for the following:

| Surface | Retained identity |
| --- | --- |
| Envelope `protocol` | `open-agent-protocol` |
| Envelope `profile` | All existing profile strings, including `open-agent-protocol.agent-control-core` and `open-agent-protocol.model-provider-core` |
| Hub transport events | `oap-subscribed`, `oap-overflow`, `oap-replay-gap`, `oap-session-closed`, `oap-frame-limit`, `oap-stream-failed` |
| Capability descriptors | The `capability_revision` member and existing revision values, including harness pins containing `-oap-` |

These are compatibility identities, not display labels. This naming decision
does not change envelope types, hub operations, feature keys, schema constants
or conformance claims. It does not add `oxap-*` hub event aliases.

A genuine capability or harness-version change may still publish a new
revision under the existing catalog and refresh rules. Branding alone does
not justify rewriting a pinned revision. A future wire rename requires its
own versioned compatibility decision; it is outside this v0.1 rename.

### Local names migrate with fallback compatibility

Later implementation changes must preserve these boundaries:

- User-facing `OXAP_*` variables take precedence over their legacy `OAPX_*`
  equivalents; read the old name only when the new name is absent. The SDK's
  `OAP_SDK_BINARY_PATH`, `OAP_SDK_BINARY_URL` and `OAP_SDK_BINARY_SHA256` follow
  the same rule with `OXAP_SDK_BINARY_*`. Internal test gates need no bulk rename.
- The new user-data location is `~/.oxap`. When it is absent, retain read
  access to existing `~/.oapx` state. Workspace `.oxap/` follows the same
  fallback rule for `.oapx/`. Do not automatically move or delete legacy state.
- Keep the Keychain service `ai.hyperneo.oap`. Do not copy credentials into
  a plain file or automatically move `auth.json` as part of renaming paths.
- The loop's namespaced metadata may later accept `oxap` alongside `oapx`.
  Readers must accept the new spelling before writers switch; reject
  conflicting values rather than silently choosing one. The current `oapx`
  spelling remains supported. This decision alone changes neither reader nor
  writer.

Each migration needs its own observable compatibility proof before examples
or installation instructions switch to the new names. No global replacement
of `OAP`, `oap` or `oapx` is authorized by this decision.

### Repository, publication and historical names

The intended future repository is `oxap-dev/oxap`; intended publication names
are `oxap` and `@oxap-dev/cli-*` on npm, `oxap-sdk` on PyPI, `oxap-*` crates,
and the Go module path `oxap.dev`. These are migration targets, not claims
that accounts, domains or package names have been acquired or published.
Repository transfer, domain setup and registry publication require separate
owner actions. Go imports change only after the vanity endpoint is working;
existing imports and published versions are not rewritten by this decision.

Keep identifiers in `research/` ledgers and `fixtures/adapters/` corpora as
recorded. They describe pinned upstream vocabulary and wire evidence, not
project branding. Historical references remain valid under their old names.

## Consequences

This change adds the naming record and one README notice. Schemas, validators,
SDKs, harness catalogs, binaries, release assets and package manifests keep
their current behavior and names. CLI aliases, configuration fallback and
publication changes follow as separate, small changes with compatibility
evidence. The documentation site is also a separate implementation task.
