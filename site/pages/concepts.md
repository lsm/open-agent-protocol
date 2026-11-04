# Concepts

Read these concepts as software boundaries, not deployment locations:

- [Composition](../source/drafts/composition.md): what each layer may choose about the layer below it.
- [Layered model](../source/drafts/layered-agent-protocol.md): presentation, control, agent loop, providers and tools.
- [Agent control](../source/drafts/agent-control-core.md): sessions, admission, runs and interactions.
- [Hub](../source/drafts/hub.md): multiplexing, ownership and replay.
- [Work](../source/drafts/work.md): managing work over sessions.

Replay of journaled events, reopening a bound session, and reading saved transcripts have different contracts. Follow the linked drafts and their decision amendments rather than treating the words as synonyms.

::: warning Open conflict — C14–C21
The Hub introduction and subscription rules disagree with parts of the implementation. C16–C18 cover the staged connection-lifetime, keepalive and cursor changes; [both sides remain open](../conflicts.md).
:::
