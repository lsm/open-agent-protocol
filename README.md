# Open Agent Protocol

Draft public-domain semantic protocol for agent runtimes, tools, model IO, and
UI/controller adapters.

The first conformance target is a UI-to-runtime core covering capabilities,
session state, run lifecycle, streamed content, tools, permissions, user-input
prompts, and transcript sync without binding to a specific transport.

Current drafts:

- [Conformance Draft](drafts/conformance.md)
- [UI Runtime Core](drafts/ui-runtime-core.md)
- [UI Runtime Protocol](drafts/ui-runtime-protocol.md)
- [Layered Agent Protocol](drafts/layered-agent-protocol.md)

Draft contract and fixtures:

- [TypeScript core contract](types/core.ts)
- [TypeScript protocol contract](types/protocol.ts)
- [Example protocol envelopes](examples/README.md)

The repository is dedicated under CC0-1.0 so any UI, runtime, or adapter can implement the protocol without project-specific licensing friction.
