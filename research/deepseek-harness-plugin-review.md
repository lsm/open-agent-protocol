# DeepSeek Harness Plugin Architecture Review

Status: research note

Reviewed repository: `deepseek-ai/deepseek-harness`

Reviewed commit: `47f943859bef60e4160492346772ded9b24f765a`

Review date: 2026-08-14

Local checkout: `/Users/lsm/focus/tmp/oap-references/deepseek-harness`

This note evaluates DeepSeek Harness as an architectural reference for Open
Agent Protocol. It focuses on its Cordis plugin system, composition model,
capability seams, per-agent scoping, lifecycle, UI extension model, and dynamic
agent-authored packages. It does not propose compatibility with DeepSeek
Harness or make its implementation vocabulary part of OAP.

## Executive Summary

DeepSeek Harness and OAP are solving adjacent problems in different
dimensions:

- DeepSeek Harness standardizes composition inside one implementation. Its
  plugins can provide services, register tools and prompt sections, intercept
  events, replace providers, and contribute UI behavior.
- OAP standardizes semantic boundaries between independently implemented
  software layers, regardless of process, language, framework, or transport.

The strongest lesson is not that OAP should make everything a plugin. It is
that OAP capabilities should be composable, scoped, attributable, revisioned,
and lifecycle-aware. A plugin system may implement those semantics, but plugin
module names and runtime APIs must not become protocol identities.

DeepSeek Harness exposes one real gap in OAP core: capabilities are currently
described as effective facts but have no explicit revision or change model.
That is insufficient when tools, model adapters, policies, or other providers
can be mounted, unmounted, replaced, or scoped per session.

Most other lessons belong in a future composition/control-plane profile, not
in agent-control core.

## What DeepSeek Harness Actually Implements

### 1. A plugin is a lifecycle-owned contribution

Cordis accepts function, class, or object plugins. A plugin may declare:

- `inject`: required services;
- `provide`: services it supplies;
- `Config`: runtime configuration validation;
- `apply`: activation behavior.

Each application of a plugin creates a `Fiber`. The fiber owns its validated
configuration, dependency state, event listeners, provided services, nested
plugins, and cleanup effects. Disposal unwinds registrations rather than
leaving each subsystem to invent teardown.

This is more important than the package format. The useful abstraction is
"a contribution has an owner and a reversible lifecycle."

### 2. Services provide stable internal capability names

A Cordis `Context` is a scoped service repository. Consumers request stable
service keys such as `tools`, `llm`, `sessions`, `fs`, or `shell`; they do not
normally import a concrete provider.

Required service injection controls activation. A plugin remains pending when
its dependencies are absent and can activate when they appear. Service
isolation lets another provider occupy the same service key in a child scope.

This resembles OAP profiles and capabilities, but it is an in-process
TypeScript contract, not an interoperable protocol contract.

### 3. A capability seam has three roles

DeepSeek Harness explicitly models a capability seam as:

1. a service definition;
2. a service provider;
3. a consumer, often a model-facing tool.

This is a useful design test for OAP profiles. Defining only a provider API is
not enough. A complete boundary must say what is provided, how consumers
discover and invoke it, and how absence, replacement, degradation, and errors
are represented.

### 4. Composition is an ordered, patchable plugin tree

A running harness is assembled from:

- a named profile;
- ordered bundles;
- profile-local patches;
- home-level patches;
- command-line overlays.

Rows have stable configuration IDs. Later layers may replace a row's config or
insert more rows. `dsh --dump-config` exposes the resolved tree.

This gives operators an inspectable effective composition and deterministic
precedence. The YAML row IDs and npm module names are implementation details,
but resolved configuration plus provenance is a protocol-worthy idea.

### 5. Composition can be scoped per agent/session

Agent presets mount a standing plugin composition and join individual agents
to it through scoped inheritance. A child agent can join the exact generation
used by its parent rather than re-resolving a possibly changed preset.

The implementation also rejects preset compositions that leak services into
the process-global realm. A preset replacement is restricted once session
history depends on the old tool and prompt environment.

This demonstrates that effective capabilities are not necessarily
connection-global. They may be scoped to a profile, session, agent, child
agent, or run, and a session may need to remain pinned to one composition
generation for replay and correctness.

### 6. Durable facts and live interception are separate

DeepSeek Harness separates:

- durable session events, used for replay, resume, UI projection, and model
  history;
- live agent events, used to intercept work in flight;
- capability events, used to attach providers and policy.

Its rule "model-visible means logged" makes model input reconstructable from
the durable session stream. The rule is stronger than OAP core should require,
but it is a good basis for a future replay/audit conformance unit.

### 7. Event dispatch semantics are explicit

Cordis distinguishes observation, concurrent observation, ordered decisions,
and around-middleware through `emit`, `parallel`, `serial`, and `waterfall`
dispatch modes. Waterfall listeners must delegate explicitly and may
short-circuit.

OAP should not copy these modes into ordinary wire events. They matter only if
OAP later defines extension hooks or interception points. At that boundary,
ordering, mutation, short-circuiting, timeout, and failure behavior must be
part of the hook contract rather than implicit framework behavior.

### 8. Host and presentation contributions can travel together

The web application itself is a plugin composition. Client packages register
UI slots, renderers, commands, settings panels, and transport consumers. Some
dynamic packages have coordinated host and browser halves with one stable
plugin identity and immutable package versions.

This shows a legitimate future OAP use case: one extension may contribute to
multiple semantic boundaries. It does not imply that OAP should standardize
React components or downloadable JavaScript. The portable contract should
describe contributions and required host capabilities; each binding or SDK can
choose its executable packaging model.

### 9. Dynamic agent-authored packages are versioned state machines

The optional Cordis toolset lets an agent:

- inspect approved runtime contracts;
- define immutable package versions;
- activate, update, stop, and remove a plugin;
- correlate each activation with a stable run identity;
- preserve the last successful version while another version is pending or
  failed;
- report host, client, approval, and render failures separately.

This is a good lifecycle model. It treats definition, approval, activation,
commit, rollback target, and execution as different states.

Its security stance is intentionally limited. Host code runs in `node:vm` with
a guarded facade, but the source states that this is not containment. Normal
npm profile plugins are fully trusted executable code. OAP must not confuse a
permission manifest with actual sandboxing.

## Mapping To OAP

| DeepSeek Harness concept | Closest OAP concept | OAP lesson |
| --- | --- | --- |
| Cordis service key | Profile or named capability | Keep semantic identity independent from provider implementation. |
| Service provider | Layer implementation or adapter | Report the effective provider behavior and degradation. |
| `inject` requirement | Required capability/profile | Requirements need versions, support constraints, and failure semantics. |
| Fiber | Owned component activation | Contributions need explicit lifecycle and cleanup ownership. |
| Context isolation | Capability scope | Capabilities may differ by connection, session, agent, or run. |
| Profile/bundle/patch | Composition plus overlays | Expose resolved configuration and provenance without standardizing one config syntax. |
| Agent preset | Session composition | Pin sessions and child agents to a composition generation. |
| `tools/change` and provider replacement | Capability change | Add capability freshness and update semantics. |
| Session event log | Canonical durable event stream | Distinguish replayable facts from ephemeral output and hooks. |
| Waterfall event | Interception hook | If standardized later, define order, delegation, cancellation, and failure. |
| Client plugin slot | Presentation contribution | Standardize semantic contribution descriptors before any executable UI ABI. |
| Dynamic package version/run | Extension definition and activation | Separate immutable package identity from activation attempt and current version. |

## Gaps In The Current OAP Core

### 1. Capability freshness is undefined

OAP capabilities describe effective behavior and support levels, but the core
does not currently define:

- a capability snapshot revision;
- the scope in which that revision is authoritative;
- how a peer learns that capabilities changed;
- whether a session or run is pinned to an earlier capability set;
- what happens when a capability disappears during an accepted operation.

This matters even without a general plugin system. Tools can reconnect, model
providers can lose authentication, policies can change, adapters can discover
new support, and remote endpoints can degrade.

Recommended core direction:

- add `capability_revision` to `capabilities.response`;
- make its scope explicit, initially connection or endpoint;
- let subsequent requests optionally name the revision they were gated from;
- define stale-capability rejection as a typed error;
- add an optional `capabilities.updated` event or a small
  `+dynamic-capabilities` unit;
- report the effective capability revision on session/run admission when it
  can differ from the connection snapshot.

The core should not require dynamic mutation. A static implementation can keep
one revision for its lifetime.

### 2. Scope exists in examples but lacks a capability inheritance model

OAP already carries connection, session, run, turn, and tool-call identities.
It does not say whether a session capability descriptor inherits from a
connection descriptor, replaces it, or applies a delta.

Core only needs a clear authority rule. Rich inheritance and per-agent
composition belong later.

### 3. Accepted work is not explicitly pinned to effective dependencies

OAP reports effective model and delivery decisions, but not the effective tool
catalog revision, policy revision, workspace identity, or broader composition
generation that admitted a run.

This should remain optional in core, but admission metadata needs an extension
path for deterministic recovery and audit.

### 4. Durable and ephemeral semantics need sharper classification

OAP distinguishes canonical session state, transcript synchronization, and
live content events, but does not yet provide a general statement about which
facts must be replayable. A future persistence/replay unit should classify:

- canonical durable facts;
- reconstructable projections;
- ephemeral stream chunks;
- live interception hooks that must never be mistaken for history.

## Future OAP Design Suggested By The Review

### A composition plane, not a plugin layer

OAP should eventually describe a cross-cutting composition plane. It should
not be inserted between Control and Agent Loop as another runtime layer.
Composition selects implementations and contributions for existing logical
boundaries.

A future composition profile could define:

`ComponentDescriptor`

- stable component identity;
- implementation/version identity;
- human metadata;
- provenance and trust classification;
- configuration schema reference;
- declared permission/resource footprint.

`ContributionDescriptor`

- semantic profile or capability provided;
- profile version and conformance units;
- support level and degradation;
- scope and cardinality;
- priority or selection constraints;
- lifecycle ownership.

`RequirementDescriptor`

- required semantic capability/profile;
- compatible version range;
- minimum support level;
- required or optional status;
- same-scope or inherited-scope requirement.

`CompositionSnapshot`

- stable composition ID and revision;
- selected components and contributions;
- resolved effective configuration;
- override provenance;
- conflicts, missing requirements, and degradation;
- scope and parent composition when applicable.

`Activation`

- activation ID;
- desired and effective component version;
- phase such as pending, active, failed, draining, or disposed;
- missing requirements;
- diagnostics;
- rollback/current-version relationship.

### Provider selection and cardinality

Different seams have different rules:

- one active session persistence provider;
- many model routes;
- many tools keyed by name;
- ordered policy interceptors;
- one renderer for a keyed presentation contribution;
- many observational listeners.

A future composition profile must represent `single`, `keyed-many`,
`ordered-many`, and `broadcast-many` semantics. A generic list of plugins is
not enough.

### Configuration and provenance

OAP should eventually distinguish:

- requested configuration;
- resolved effective configuration;
- defaults supplied by an implementation;
- policy overrides;
- secrets that are accepted but never returned;
- immutable session/run snapshots;
- configuration changes that require restart or re-composition.

DeepSeek Harness's layered patches show why provenance matters. An effective
value without its owner and override source is difficult to debug.

### Trust, permissions, and execution isolation

A future component descriptor should declare requested powers such as:

- filesystem scopes and mutation;
- process execution;
- network destinations;
- credential references;
- model-visible prompt or tool contribution;
- durable storage;
- presentation code execution;
- control or interception hooks.

These declarations support policy and approval. They do not prove isolation.
The protocol must report enforcement mode separately: native sandbox,
process/container isolation, cooperative restriction, or unrestricted trusted
code.

### Extension inspection

DeepSeek Harness's inspect providers are a useful pattern. A portable future
profile could expose read-only introspection for:

- active components;
- provided and required capabilities;
- configuration schema and redacted effective configuration;
- lifecycle state and missing requirements;
- contribution ownership;
- degradation and diagnostics.

Presentation should render this information generically. It must still gate
product features from semantic capabilities, not module or plugin names.

## What OAP Should Not Copy

### Do not make npm packages or module names protocol identities

`@deepseek-ai/dsh-*`, Cordis service keys, Loader row IDs, and YAML patches are
valid implementation details. OAP identities must survive another language,
package manager, process model, and transport.

### Do not standardize one executable plugin ABI in core

An in-process TypeScript plugin, a WASI component, a child process, a remote
service, and a static linked module can all provide the same OAP capability.
The semantic contract should be portable; executable loading is a binding or
deployment concern.

### Do not expose generic middleware in agent-control core

Provider-specific interception, request rewriting, and short-circuit hooks can
break invariants that ordinary control clients rely on. Standardize named hook
contracts only when the ownership, order, mutation rights, and recovery rules
are understood.

### Do not make UI components part of presentation-control core

Presentation-control should continue to exchange targets, snapshots, typed
changes, intents, and affordances. Executable renderers and UI slots are a
future local SDK or extension binding, not portable presentation semantics.

### Do not treat plugin inventory as capability advertisement

DeepSeek Harness's current inventory exposes entry ID, module name, enabled
state, and fiber phase. That is useful diagnostics but insufficient for
interoperability. OAP needs semantic provided/required capabilities,
conformance, scope, degradation, and lifecycle.

## Gaps In DeepSeek Harness That OAP Can Address

These are not necessarily defects in a developer preview; they mark the
boundary between a strong framework and a portable protocol.

1. Service requirements are named but not semantically versioned.
2. Plugin and bundle manifests do not describe portable capability contracts,
   conformance units, degradation, or permission footprint.
3. Plugin inventory is implementation-oriented and too shallow for generic
   control or UI gating.
4. Normal profile plugins are trusted executable npm packages; dynamic host
   packages use guarded `node:vm` execution that explicitly is not containment.
5. Trust is coarse (`system` or `user`) and does not express separate code,
   prompt, tool, filesystem, network, credential, or UI powers.
6. Composition patches depend on implementation-specific row IDs and whole
   config replacement.
7. Host/client dual packages are coupled to DSH's TypeScript, browser, Typert,
   and Cordis environment rather than a transport-neutral contribution model.
8. Typed event contracts rely on TypeScript declaration merging and cannot be
   implemented directly by another language or remote process.
9. Dynamic topology has rich local lifecycle state but no external protocol
   conformance claim that another controller can rely on.
10. Real Loader-path postmortems show that plugin composition creates failure
    modes that unit coverage alone misses; portable conformance fixtures would
    complement its implementation tests.

## Recommended OAP Sequence

1. Keep the current agent-control minimum surface small.
2. Add capability revision and freshness semantics before finalizing core
   conformance.
3. Add a short composition-plane section to the layered draft, explicitly
   stating that it is cross-cutting and optional.
4. Put component, contribution, requirement, composition, activation, scope,
   provenance, and enforcement vocabulary in an idea-pool draft.
5. Validate that vocabulary against DeepSeek Harness, HyperNeo, Codex and
   Anthropic SDK adapters, MCP tool sources, and at least one process-isolated
   plugin system.
6. Only then decide whether composition becomes one profile or several smaller
   profiles, such as discovery, lifecycle, configuration, and presentation
   contribution.
7. Keep executable packaging and UI component APIs in binding-specific SDKs
   until independent implementations prove a portable ABI is necessary.

## Bottom Line

DeepSeek Harness validates OAP's layered direction: the agent loop, model IO,
tools, sessions, resources, policy, and presentation are real independent
boundaries even when one framework composes them in-process.

Its main addition is a missing dimension: the protocol should eventually say
how implementations of those boundaries are selected, scoped, revised,
observed, and retired. That is a composition plane. It should describe
semantic contributions and their effective lifecycle, not decree that every
implementation must use plugins.

## Primary References

- [DeepSeek Harness repository](https://github.com/deepseek-ai/deepseek-harness)
- [Architecture at the reviewed commit](https://github.com/deepseek-ai/deepseek-harness/blob/47f943859bef60e4160492346772ded9b24f765a/docs/architecture.md)
- [Cordis primer at the reviewed commit](https://github.com/deepseek-ai/deepseek-harness/blob/47f943859bef60e4160492346772ded9b24f765a/docs/cordis-primer.md)
- [Cordis registry implementation](https://github.com/deepseek-ai/deepseek-harness/blob/47f943859bef60e4160492346772ded9b24f765a/vendor/cordis/src/registry.ts)
- [Agent preset composition](https://github.com/deepseek-ai/deepseek-harness/blob/47f943859bef60e4160492346772ded9b24f765a/packages/preset/agent-presets/src/index.ts)
- [Agent preset mounting and leak checks](https://github.com/deepseek-ai/deepseek-harness/blob/47f943859bef60e4160492346772ded9b24f765a/packages/preset/agent-presets/src/mount.ts)
- [Dynamic extension subsystem](https://github.com/deepseek-ai/deepseek-harness/blob/47f943859bef60e4160492346772ded9b24f765a/docs/subsystems/extensions.md)
- [Dynamic host sandbox trust boundary](https://github.com/deepseek-ai/deepseek-harness/blob/47f943859bef60e4160492346772ded9b24f765a/packages/extensions/cordis-host-runner/src/sandbox.ts)
- [Plugin installation and bundle reconciliation](https://github.com/deepseek-ai/deepseek-harness/blob/47f943859bef60e4160492346772ded9b24f765a/apps/cli/src/plugin.ts)
