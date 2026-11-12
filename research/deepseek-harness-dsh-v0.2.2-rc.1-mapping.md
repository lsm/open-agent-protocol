# DeepSeek Harness dsh-v0.2.2-rc.1 release-candidate mapping

This release-candidate pin extends the [dsh-v0.2.1-rc.2 ledger](deepseek-harness-dsh-v0.2.1-rc.2-mapping.md). No stable dsh release is asserted. Source: `https://github.com/deepseek-ai/deepseek-harness`, tag `dsh-v0.2.2-rc.1`, commit `66491b05e3f05ad35bcdd4f6cd7ed26041d9e3db`, tree `3aeac580e803b0ce82f867b1374ed494a59662c2`. The source comparison was read-only; no account, model, or live process was used.

## Wire review

The SDK retains line-delimited JSON-RPC and the same `0.0.1` server identity. `initialize` adds optional `approvalRequests` and `capabilities.userQuestions`. The server enables each only when explicitly true. The adapter sends neither: `approval/request`, `user-question/ask`, and `user-question.cancelled` are not enabled or mapped. Interactive user-question and approval forwarding are **not verified** and are not advertised by this adapter. The new `session/query` method and `session.query.completed` notification implement command queries; the adapter continues to use `session/prompt` and does not claim command support. New image `name` is optional and is not emitted. The line transport additionally rejects requests after closure and supports aborting its own pending calls; the existing adapter does not depend on reusing a closed transport.

The server now identifies subagents using the session origin instead of parent presence alone. This limits notifications for ordinary forks; it does not make those forks child runs. Scheduled restoration, command activity and SDK descendants are not verified. The existing known-event-types and LLM message and assistant stream sources are unchanged. The added `model/selection` event describes a model selection and is already treated as observed-only by the adapter. `turn/end` can now report `TOOL_ACTIVATION_FAILED`; the synthetic `turn-end-reasons` case exercises that error shape and retains the existing generic `deepseek_error` mapping. No live feature, runtime startup, or model behavior was verified.

## Source blobs

| Source | Git blob |
| --- | --- |
| `packages/sdk/protocol/src/types.ts` | `6bcd2de7045e58c0fab0e5bacf956724d1ff6e68` |
| `packages/sdk/protocol/src/transport.ts` | `4ee65555c0342b276e43b03186659acb623cb6fe` |
| `packages/sdk/server/src/server.ts` | `1879b23e2eeb57d080f2d38401ab5a0a81ba03d7` |
| `packages/sdk/server/src/user-questions.ts` | `fab6916c39810d3ba17c037af898f40a29a1ed73` |
| `packages/sdk/server/src/query-activity.ts` | `17a4fc9148e5f456c5e9e480cb71607cffeb0b24` |
| `packages/interaction/user-questions/src/types.ts` | `25340a3009ec182352e83aa0ba1c6cd8a25037f9` |
| `packages/bundle/sdk-minimal/cordis.patch.yml` | `e46eed1e0fb6520a48e7b9929c5ca343c2e2deba` |
| `packages/core/session/src/types.ts` | `7787dd9bd073217825356b9c6b419d2e153d2537` |
| `packages/core/session/src/known-event-types.ts` | `a0f5c17795be074e3c521c525a0eb59ee25248d6` |
| `packages/core/agent/src/types.ts` | `06d2c88ab1e5ada455214555aaae542077caa571` |
| `packages/core/agent-loop/src/inbox.ts` | `affe0c6b2f653888dc2177fd2987c6082a93f758` |
| `packages/core/agent/src/runtime-types.ts` | `2b1698e39c663f4d8d7760ebdaef647bed693257` |
| `packages/core/agent-loop/src/agent.ts` | `c4daa50c47c13fb86b94466774dfcac32b214076` |
| `packages/llm/llm/src/message.ts` | `d96a63edb6d78d64508875ebc1cc6a1cc5a824b6` |
| `packages/llm/llm/src/types.ts` | `8a48b89895db0ddb8908ec6359eb67af60fe0f54` |
| `packages/llm/llm/src/assistant-stream.ts` | `7fca34c5d5bf432f7fad81bd48edfbdaf5083e83` |
| `packages/core/session/src/surface.ts` | `0bb84c030ef595b3d9a17116e7dc7b9deb26369c` |

The new corpus carries prior fixtures with their original provenance and updated OAP capability revision. Its `turn-end-reasons` case is a synthetic derivative with the new error code; its source provenance names this release, not a recording. A source review and synthetic corpus cannot establish full live compatibility. In particular question/approval behavior, commands, scheduling, descendants, and end-to-end execution remain not verified.

The Rust DeepSeek port implements native decoding, process support, the adapter's descriptor and launch, the native prompt translation and the content mapping, not yet full adapter trace replay. Rust native notification decoding was compared against the new corpus, while full OAP trace agreement is established by Go and Zig only.

Two Go/Rust differences are recorded rather than copied. First, tool-call arguments that are not JSON. Both trees refuse them while decoding the native assistant message (`carriesIntoATrace` in Go, `carries_into_a_trace` in Rust), so they never reach the final-response mapping. In Go the notification fails to decode, the client closes, and the active run fails as `deepseek_process_exit`. Native decoding refuses an empty arguments string too. The difference is only in the mapping called on its own, and only for non-empty arguments that are not JSON: Go's `blocksContent` passes them through, and `PartsContent` drops its own marshal error, so the content would be `null`, while Rust's `final_content` returns an error. An empty arguments string is omitted from the tool-call part on both sides. The Rust session that would turn that error into a run failure is a later slice. No corpus case carries non-JSON tool-call arguments.

Second, Rust's shared content part type refuses a `null` member inside a content part, such as a text part whose `text` is `null`, which Go reads as an empty string. Only a schema-invalid submit carries one, so no valid trace is affected.
