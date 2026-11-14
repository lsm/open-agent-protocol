use std::io::Cursor;

use oxap_conformance::{read_frame, write_frame, Frame, Transcript};
use oxap_protocol::{Envelope, EnvelopeType};
use serde_json::json;

fn envelope(kind: &str, id: &str, payload: serde_json::Value) -> Envelope {
    Envelope::new(EnvelopeType::from(kind), id, &payload).unwrap()
}

#[test]
fn controls_envelopes_blank_lines_and_a_final_unterminated_line_round_trip() {
    let mut input = Cursor::new(concat!(
        "\n \t\n",
        "{\"control\":\"replay\",\"id\":\"c\",\"after\":0,\"latest_available\":18446744073709551615}\n",
        "{\"protocol\":\"open-agent-protocol\",\"type\":\"capabilities.response\",\"id\":\"e\",\"payload\":{}}"
    ));
    let control = read_frame(&mut input).unwrap().unwrap();
    let Frame::Control(frame) = &control else {
        panic!("expected a control answer");
    };
    assert_eq!(frame.after, Some(0));
    assert_eq!(frame.latest_available, u64::MAX);
    let mut written = Vec::new();
    write_frame(&mut written, &control).unwrap();
    let answer: serde_json::Value = serde_json::from_slice(&written).unwrap();
    assert_eq!(answer["after"], 0);
    assert_eq!(answer["latest_available"], u64::MAX);
    assert!(answer.get("requested_after").is_none());
    let Frame::Envelope(frame) = read_frame(&mut input).unwrap().unwrap() else {
        panic!("expected an OAP answer");
    };
    assert_eq!(frame.id.as_str(), "e");
    assert!(read_frame(&mut input).unwrap().is_none());
}

#[test]
fn malformed_output_is_an_error_and_protocol_wins_over_a_control_member() {
    for input in [
        "{",
        "[]",
        "1",
        "{\"control\":7}",
        "{\"control\":\"replay\",\"after\":-1}",
    ] {
        assert!(read_frame(&mut Cursor::new(input)).is_err(), "{input}");
    }
    let text = "{\"protocol\":\"open-agent-protocol\",\"control\":\"replay\",\"id\":\"e\"}";
    assert!(matches!(
        read_frame(&mut Cursor::new(text)).unwrap(),
        Some(Frame::Envelope(_))
    ));
    let text = "{\"control\":\"replay\",\"id\":null,\"latest_available\":null}";
    let Some(Frame::Control(frame)) = read_frame(&mut Cursor::new(text)).unwrap() else {
        panic!("expected a nullable control answer");
    };
    assert!(frame.id.is_empty());
    assert_eq!(frame.latest_available, 0);
}

#[test]
fn a_trace_places_early_events_after_admission_and_keeps_unadmitted_events() {
    let mut trace = Transcript::default();
    let mut early = envelope("run.started", "early", json!({}));
    early.run_id = Some("r".into());
    early.sequence = Some(0);
    trace.push(early.clone());
    trace.push(early);
    let mut orphan = envelope("run.started", "orphan", json!({}));
    orphan.run_id = Some("orphan-run".into());
    orphan.sequence = Some(0);
    trace.push(orphan);
    trace.push(envelope(
        "session.message.submit.response",
        "admission",
        json!({"run_id":"r"}),
    ));
    trace.push(envelope("run.completed", "tail", json!({})));
    assert_eq!(
        trace
            .envelopes()
            .iter()
            .map(|e| e.id.as_str())
            .collect::<Vec<_>>(),
        ["admission", "early", "tail", "orphan"]
    );
}

#[test]
fn a_trace_excludes_probe_answers_and_recognizes_open_message_admission() {
    let mut trace = Transcript::default();
    trace.probe("probe".into());
    let mut refused = envelope("error.response", "refusal", json!({}));
    refused.in_reply_to = Some("probe".into());
    trace.push(refused);
    let mut early = envelope("run.started", "early", json!({}));
    early.run_id = Some("r".into());
    early.sequence = Some(0);
    trace.push(early);
    trace.push(envelope(
        "session.open.response",
        "opened",
        json!({"active_run_id":"r"}),
    ));
    assert_eq!(
        trace
            .envelopes()
            .iter()
            .map(|e| e.id.as_str())
            .collect::<Vec<_>>(),
        ["opened", "early"]
    );
    assert_eq!(trace.envelopes().len(), 2);
}
