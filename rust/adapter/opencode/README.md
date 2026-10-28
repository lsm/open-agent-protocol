# oap-adapter-opencode

The Open Agent Protocol adapter over the OpenCode server, ported from
`go/adapter/opencode` and judged against `fixtures/adapters/opencode-*`.

`sse` is the event-stream frame decoder the transport reads with: CRLF or LF
line endings, a bare CR refused, one `event` and one `id` per frame, a frame
without data refused, and a byte limit per frame. Unlike Go's decoder it
refuses a frame that is not UTF-8 instead of passing the bytes on, since the
JSON decode that follows would refuse it anyway.
