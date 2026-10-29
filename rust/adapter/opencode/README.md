# oap-adapter-opencode

The Open Agent Protocol adapter over the OpenCode server, ported from
`go/adapter/opencode` and judged against `fixtures/adapters/opencode-*`.

`sse` is the event-stream frame decoder the transport reads with: CRLF or LF
line endings, a bare CR refused, one `event` and one `id` per frame, a frame
without data refused, and a byte limit per frame. Unlike Go's decoder it
refuses a frame that is not UTF-8 instead of passing the bytes on, since the
JSON decode that follows would refuse it anyway.

`native` holds the HTTP API's types with Go's member sets: every response
type refuses an unknown member, as Go's `DisallowUnknownFields` does at every
depth. Two of Go's rules belong to the client that reads and writes these
types, and land with it: a response with a repeated key is refused through
`oap_adapter::gojson::reject_duplicate_keys`, and a request body is written
through `oap_adapter::gojson::go_marshal`, so `<`, `>` and `&` are escaped as
Go writes them.

`native::decode_event` reads one event-stream frame as Go's `DecodeEvent`
does. It refuses a repeated key, reads an unpaired surrogate escape as U+FFFD,
and admits a session event only with its session id, a pinned type and the
durable position that type declares, or without one if the type is ephemeral.
