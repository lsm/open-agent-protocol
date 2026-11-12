# oap-adapter-deepseek

The Rust port of `go/adapter/deepseek`: an OAP adapter over the DeepSeek
harness SDK runtime's JSON-RPC on stdio, pinned by
`harnesses/deepseek-harness.json` and judged by the corpus in
`fixtures/adapters/deepseek-harness-dsh-v0.2.2-rc.1`.

So far it holds the native handshake, prompt, notification and event payload
types, including Go's `MessageSource` rules for closed source kinds and its
assistant stream records with the chunk shapes they carry, Go's
`Event.Validate` for every pinned event type, `DecodeNotification` and the
handshake and prompt validators, and Go's
`DecodeStrict`: a repeated key is refused, an unknown member is refused, a
null reads as the zero value, a null array element reads as its zero value,
each invalid UTF-8 byte and each unpaired surrogate escape reads as U+FFFD,
and a non-object refused as a struct names the Go type. The handshake and
prompt results decode leniently, as Go's client does. Three of Go's
`encoding/json` rules differ:

- Go falls back to a case-insensitive key match, so `{"sessionid":"s"}` sets
  `SessionID`; these types match keys exactly. The pinned corpus spells every
  key in its exact case, so nothing observes the difference.
- Go's `json.Marshal` escapes `<`, `>`, `&`, U+2028 and U+2029, and serializing
  a type here does not; the rpc layer writes every frame through
  `oap_adapter::gojson::go_marshal`.
- A malformed document or a mistyped member is refused with the duplicate-key
  walker's `invalid JSON` or with serde_json's wording rather than Go's. A
  trailing value carries Go's `trailing JSON value`.

`process::Process` is Go's launcher: it validates the `initialize` params,
speaks the shared JSON-RPC client in its strict `deepseek` dialect with every
notification checked by `decode_notification`, and takes the server info from
the first `initialize` response, refusing any observation before it. A `null`
result decodes as an empty one, as Go's does. It closes by asking for
`shutdown`, releasing stdin only once the server acknowledges it, and kills the
child at the shutdown timeout.

`adapter::Adapter` holds what Go's `adapter.go` decides before a session
exists. `Adapter::new` refuses a missing executable, a relative working
directory and initialize params Go's validator refuses, and builds the
`initialize` params from the working directory, provider, model and token
cap. `describe` returns Go's descriptor for the catalog revision: the same
features, levels, reasons and modes, a 256-entry session journal by default,
no interactive gates and no cancellation. `launch` turns an open request into
the process configuration that session would start: the reasoning level
becomes `reasoningEffort` when the DeepSeek route takes it, and a compaction
policy becomes the `--patch` layer `compaction_patch` writes. Go also accepts a
caller-supplied client factory, which refuses either setting; the Rust adapter
always starts its own process, so it has no such branch. The session itself,
and the `Adapter` contract over it, are later slices.

`prompt::native_prompt` is Go's `nativePrompt`: it turns an OAP submit into the
native text blocks `session/prompt` carries. It refuses any unadvertised
control, including a `queue`, `steer` or `btw` delivery, then refuses as an
invalid submission an unscoped or empty request, an unknown delivery, a
message that is not from the user, and any content that is neither a string
nor a list of text parts. A null content is an empty string, as Go decodes it,
and a missing one is refused, as in Go.
A message without an id gets one minted from the session's id source.

`content` holds the session's pure content mapping. `stream_parts` expands
assistant stream records into OAP deltas: text and reasoning chunk lists, and a
`chunk` record whose strictly decoded delta is text or reasoning, a null
member decoding as its zero value as in Go; every other
record carries no delta. `final_content` turns a completed turn's blocks into
the final response, a lone text block as a string and anything else as parts,
resolving each tool-call block through the run's tools and refusing other
block kinds with Go's wording. It also refuses tool-call arguments that are
not JSON. Native decoding already refuses those in both trees, so the check
matters only when the mapping is called on its own: there Go's `blocksContent`
yields `null` content instead, which the ledger records.
