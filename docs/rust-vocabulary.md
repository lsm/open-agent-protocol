# Open vocabulary values

Protocol vocabulary values preserve future strings when decoding and encoding.
`is_known()` reports whether a value matches one of its type's declared constants.
An empty default value is unknown. `EnvelopeType` uses protocol declarations;
loaded-pack validation checks extension ownership separately.

```rust
use oap_protocol::EnvelopeType;

assert!(EnvelopeType::RUN_STARTED.is_known());
let future = EnvelopeType::from("com.example.future.event");
assert!(!future.is_known());
assert_eq!(serde_json::to_value(future).unwrap(), "com.example.future.event");
```
