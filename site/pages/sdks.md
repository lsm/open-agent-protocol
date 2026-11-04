# SDKs and clients

Choose a client for the wire you intend to use:

| Language / layer | Source guide |
| --- | --- |
| TypeScript high-level SDK, owned stdio process | [SDK README](../source/sdk/typescript/README.md) |
| TypeScript Hub client, HTTP/SSE | [Hub client README](../source/clients/ts/README.md) |
| Go endpoint SDK and native library | [Go library](../source/docs/go-library.md) |
| Rust endpoint SDK | [Rust SDK](../source/rust/sdk/README.md) |
| Python endpoint SDK | [Python SDK](../source/sdk/python/README.md) |
| Zig runtime embedding and stream ownership | [Ownership guide](../source/docs/zig-stream-memory-ownership.md) |

The endpoint SDKs and Hub clients are distinct APIs. Source compatibility names such as `MakaiClient`, package names and commands retain their spelling.

::: warning Open conflict — C02–C04, C16, C35
Go SDK tools/options and library inventories, Go loop progress, the TS Hub subscription lifetime and TS installation guidance have [unresolved differences](../conflicts.md). Both claims are preserved on the affected source pages.
:::
