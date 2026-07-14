# Open Agent Protocol

Draft public-domain semantic protocol for agent software layers, agent loops,
model IO, tools, resources, and adapters for existing agent SDKs.

The first conformance target is an agent-control core covering the boundary
between a control layer and an agent loop: capabilities, session state, message
submission, run lifecycle, streamed content, cancellation, and state recovery
without binding to a specific transport.

OAP layers are logical software boundaries, not deployment sides. A
presentation layer, control layer, agent loop, model provider, tool executor,
resource provider, and binding can live in one process or across multiple
transports while preserving the same protocol semantics. Adapters for existing
SDKs or protocols are implementation shims, not a separate semantic layer.

Current drafts:

- [Conformance Draft](drafts/conformance.md)
- [Presentation Control Profile](drafts/presentation-control-profile.md)
- [Agent Control Core](drafts/agent-control-core.md)
- [Agent Control Profile](drafts/agent-control-profile.md)
- [Layered Agent Protocol](drafts/layered-agent-protocol.md)

Fixtures:

- [Example protocol envelopes](examples/README.md)

The repository is dedicated under CC0-1.0 so any presentation layer, control
layer, agent loop, model provider, tool executor, resource provider, tool
source, or SDK adapter can implement the protocol without project-specific
licensing friction.
