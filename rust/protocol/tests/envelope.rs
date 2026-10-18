use oap_protocol::{decode, encode, Envelope, EnvelopeType, Error, Format};
use serde::{Deserialize, Serialize};
use serde_json::Value;

const HEAD: &str = r#""protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core""#;

#[test]
fn an_unknown_member_and_an_unknown_payload_member_survive_a_round_trip() {
    let input = format!(
        r#"{{{HEAD},"type":"content.delta","id":"e1","session_id":"s1","run_id":"r1","sequence":1,"future":{{"enabled":true}},"payload":{{"session_id":"s1","run_id":"r1","part":{{"type":"text","text":"hello"}},"payload_future":7}}}}"#
    );
    let envelope = Envelope::parse(input.as_bytes()).unwrap();
    assert!(envelope.unknown.contains_key("future"));
    assert_eq!(envelope.envelope_type, EnvelopeType::CONTENT_DELTA);
    let payload: Value = envelope.decode_payload().unwrap();
    assert_eq!(payload["part"]["text"], "hello");

    let output: Value = serde_json::to_value(&envelope).unwrap();
    let original: Value = serde_json::from_str(&input).unwrap();
    assert_eq!(output, original);
}

#[test]
fn a_trace_decodes_as_one_object_an_array_or_jsonl() {
    let one = format!(r#"{{{HEAD},"type":"capabilities.request","id":"e1","payload":{{}}}}"#);
    let two = one.replace(r#""e1""#, r#""e2""#);
    for (name, data, want) in [
        ("single", one.clone(), 1),
        ("array", format!("[{one},{two}]"), 2),
        ("jsonl", format!("{one}\n\n{two}\n"), 2),
        ("empty", "  \n".to_owned(), 0),
    ] {
        assert_eq!(decode(data.as_bytes()).unwrap().len(), want, "{name}");
    }
}

#[test]
fn a_jsonl_error_names_its_line() {
    let one = format!(r#"{{{HEAD},"type":"capabilities.request","id":"e1","payload":{{}}}}"#);
    let err = decode(format!("{one}\n\n{{\"id\":7}}\n").as_bytes()).unwrap_err();
    assert!(matches!(err, Error::Line { line: 3, .. }), "{err}");
}

#[test]
fn an_array_with_a_trailing_value_is_refused() {
    let one = format!(r#"{{{HEAD},"type":"capabilities.request","id":"e1","payload":{{}}}}"#);
    assert!(decode(format!("[{one}] {{}}").as_bytes()).is_err());
}

#[derive(Debug, PartialEq, Serialize, Deserialize)]
struct Submit {
    session_id: String,
    delivery: String,
}

#[test]
fn a_new_envelope_carries_its_typed_payload_and_encodes_as_jsonl() {
    let request = Submit {
        session_id: "s1".into(),
        delivery: "auto".into(),
    };
    let envelope =
        Envelope::new(EnvelopeType::SESSION_MESSAGE_SUBMIT_REQUEST, "e1", &request).unwrap();
    assert_eq!(envelope.protocol, "open-agent-protocol");
    assert_eq!(envelope.decode_payload::<Submit>().unwrap(), request);

    let mut output = Vec::new();
    encode(&mut output, std::slice::from_ref(&envelope), Format::Jsonl).unwrap();
    assert!(output.ends_with(b"\n"));
    let back = decode(output.as_slice()).unwrap();
    assert_eq!(back[0].decode_payload::<Submit>().unwrap(), request);
}

#[test]
fn json_format_refuses_anything_but_one_envelope() {
    let err = encode(Vec::new(), &[], Format::Json).unwrap_err();
    assert!(matches!(err, Error::NotOneEnvelope(0)));
}

#[test]
fn a_missing_payload_is_reported_as_missing() {
    let envelope = Envelope::parse(
        format!(r#"{{{HEAD},"type":"capabilities.request","id":"e1"}}"#).as_bytes(),
    )
    .unwrap();
    assert!(matches!(
        envelope.decode_payload::<Value>(),
        Err(Error::MissingPayload)
    ));
}

#[test]
fn an_integral_sequence_or_timestamp_is_read_in_any_spelling() {
    let base = format!(r#"{{{HEAD},"type":"session.open.request","id":"o1","payload":{{}}"#);
    for (members, sequence, timestamp) in [
        (
            r#","sequence":1.0,"timestamp_ms":1.7e12"#,
            1,
            1_700_000_000_000,
        ),
        (r#","sequence":1e2,"timestamp_ms":-0.0"#, 100, 0),
        (r#","sequence":2.50e1,"timestamp_ms":3"#, 25, 3),
    ] {
        let envelope = Envelope::parse(format!("{base}{members}}}").as_bytes()).unwrap();
        assert_eq!(envelope.sequence, Some(sequence), "{members}");
        assert_eq!(envelope.timestamp_ms, Some(timestamp), "{members}");
        assert!(!envelope.unknown.contains_key("sequence"));
    }
    for members in [
        r#","sequence":25E-1e1"#,
        r#","sequence":1.5"#,
        r#","sequence":1e41"#,
        r#","timestamp_ms":2.25"#,
        r#","sequence":"1.0""#,
        r#","sequence":-1"#,
    ] {
        assert!(
            Envelope::parse(format!("{base}{members}}}").as_bytes()).is_err(),
            "{members}"
        );
    }
}

#[test]
fn a_null_counter_or_extensions_member_reads_as_absent() {
    let envelope = Envelope::parse(
        format!(r#"{{{HEAD},"type":"run.started","id":"e1","payload":{{}},"sequence":null,"extensions":null}}"#)
            .as_bytes(),
    )
    .unwrap();
    assert_eq!(envelope.sequence, None);
    assert!(envelope.extensions.is_empty());
}
