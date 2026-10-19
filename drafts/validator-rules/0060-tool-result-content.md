# Tool result content

Decision 0060: the rows behind the diagnostic the validators raise for a tool
result that carries content parts. Go, Zig and Rust name the function in
`go/validation/`, `zig/src/validation/` and `rust/validation/src/state/`. Format:
[README](README.md).

### [0060] A result is JSON or a content-part list (OPEN-0011-2)

Source: [`decisions/0060-a-tool-result-may-carry-content-parts.md`](../../decisions/0060-a-tool-result-may-carry-content-parts.md#a-result-is-json-or-a-content-part-list-open-0011-2).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0060#a-result-is-json-or-a-content-part-list-open-0011-2/1` | A `tool_result` part's `result`, `action.call.completed`'s `result` and `action.call.resolve.request`'s `result` MAY be a non-empty list of `text` and `image` content parts, read as content in order; any other value is a JSON result. | L30–35 | — | `toolresult.go:carriesImage` | `semantic.zig:carriesImage` | `results.rs:carries_image` |

### [0060] An image part needs `content.image`

Source: [`decisions/0060-a-tool-result-may-carry-content-parts.md`](../../decisions/0060-a-tool-result-may-carry-content-parts.md#an-image-part-needs-contentimage).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0060#an-image-part-needs-contentimage/1` | An endpoint MUST NOT report a tool result carrying an `image` part unless its descriptor advertises `content.image` at a level other than `unavailable`. | L39–41 | `unavailable_capability` | `toolresult.go:checkResultImages` | `semantic.zig:checkResultImages` | `results.rs:check_result_images` |
| `0060#an-image-part-needs-contentimage/2` | A request carrying an image result to an endpoint that does not advertise `content.image` MUST be refused like any request exercising an unadvertised feature. | L41–43 | `unavailable_capability` | — | — | — |
