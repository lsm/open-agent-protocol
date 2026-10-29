use std::path::Path;

use oap_adapter_opencode::native::{
    decode_data, decode_event, durable, session_scoped, supported, valid_event_id,
    valid_session_id, Error, Event,
};
use serde_json::Value;

const STARTED: &str = r#"{"id":"evt_1","type":"session.execution.started","durable":{"aggregateID":"ses_1","seq":1,"version":1},"data":{"sessionID":"ses_1"}}"#;

fn corpus_frames() -> Vec<String> {
    let root =
        Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../fixtures/adapters/opencode-v2.0.32");
    let mut frames = Vec::new();
    for case in std::fs::read_dir(root).unwrap() {
        let Ok(native) = std::fs::read_to_string(case.unwrap().path().join("native.jsonl")) else {
            continue;
        };
        for line in native.lines().filter(|line| !line.trim().is_empty()) {
            let item: Value = serde_json::from_str(line).unwrap();
            if let Some(raw) = item.get("raw") {
                frames.push(raw.to_string());
            }
        }
    }
    frames
}

fn holds_go_s_invariants(event: &Event) {
    assert!(valid_event_id(&event.id), "{}", event.id);
    if !event.data.is_empty() {
        serde_json::from_str::<Value>(&event.data).unwrap();
    }
    if !session_scoped(&event.kind) {
        return;
    }
    assert!(supported(&event.kind) && valid_session_id(&event.session_id));
    assert_eq!(
        durable(&event.kind),
        event.durable.is_some(),
        "{}",
        event.kind
    );
    if let Some(position) = &event.durable {
        assert_eq!(position.aggregate_id, event.session_id);
        assert!(position.seq >= 0);
    }
}

#[test]
fn every_corpus_event_decodes_with_its_session_and_the_durability_its_type_declares() {
    let frames = corpus_frames();
    assert!(frames.len() > 20, "{} frames", frames.len());
    for frame in frames {
        let event = decode_event(&frame).unwrap_or_else(|error| panic!("{frame}: {error}"));
        holds_go_s_invariants(&event);
    }
}

#[test]
fn decode_event_refuses_a_key_written_twice_at_any_depth_and_admits_the_same_event_without_it() {
    for raw in [
        r#"{"id":"evt_1","type":"session.execution.started","type":"session.renamed","durable":{"aggregateID":"ses_1","seq":1,"version":1},"data":{"sessionID":"ses_1"}}"#,
        r#"{"id":"evt_1","type":"session.execution.started","durable":{"aggregateID":"ses_1","seq":1,"version":1},"data":{"sessionID":"ses_1","a":1,"a":2}}"#,
        r#"{"id":"evt_1","type":"session.execution.started","durable":{"aggregateID":"ses_1","seq":1,"seq":2,"version":1},"data":{"sessionID":"ses_1"}}"#,
    ] {
        let refused = decode_event(raw).unwrap_err().to_string();
        assert!(refused.contains("duplicate object key"), "{refused}");
    }
    let event = decode_event(STARTED).unwrap();
    assert!(valid_event_id(&event.id));
    holds_go_s_invariants(&event);
}

#[test]
fn a_session_event_needs_its_session_a_known_type_and_its_durability() {
    let refusals = [
        (
            r#"{"id":"bad","type":"server.connected"}"#,
            "invalid event id",
        ),
        (
            r#"{"id":"evt_1","type":"session.execution.started","durable":{"aggregateID":"ses_1","seq":1,"version":1},"data":{}}"#,
            "session.execution.started without a session id",
        ),
        (
            r#"{"id":"evt_1","type":"session.execution.started","data":{"sessionID":"ses_1"}}"#,
            "session.execution.started without its durable position",
        ),
        (
            r#"{"id":"evt_1","type":"session.execution.started","durable":{"aggregateID":"ses_2","seq":1,"version":1},"data":{"sessionID":"ses_1"}}"#,
            "session.execution.started without its durable position",
        ),
        (
            r#"{"id":"evt_2","type":"session.text.delta","durable":{"aggregateID":"ses_1","seq":1,"version":1},"data":{"sessionID":"ses_1"}}"#,
            "ephemeral session.text.delta with a durable position",
        ),
        (
            r#"{"id":"evt_1","type":"session.execution.started","extra":1}"#,
            "envelope",
        ),
    ];
    for (raw, detail) in refusals {
        let refused = decode_event(raw).unwrap_err().to_string();
        assert!(refused.contains(detail), "{raw}: {refused}");
    }
    assert_eq!(
        decode_event(r#"{"id":"evt_3","type":"session.future","data":{"sessionID":"ses_1"}}"#)
            .unwrap_err(),
        Error::UnsupportedType("session.future".into())
    );
    let unscoped = decode_event(r#"{"id":"evt_4","type":"server.heartbeat"}"#).unwrap();
    assert_eq!(
        (unscoped.session_id.as_str(), unscoped.data.as_str()),
        ("", "")
    );
}

#[test]
fn event_data_decodes_strictly_and_names_its_type_when_it_does_not() {
    #[derive(Debug, Default, serde::Deserialize)]
    #[serde(deny_unknown_fields)]
    struct Scoped {
        #[serde(rename = "sessionID")]
        session_id: String,
    }
    let event = decode_event(STARTED).unwrap();
    assert_eq!(decode_data::<Scoped>(&event).unwrap().session_id, "ses_1");
    let mut extra = event.clone();
    extra.data = r#"{"sessionID":"ses_1","other":1}"#.into();
    assert!(decode_data::<Scoped>(&extra)
        .unwrap_err()
        .to_string()
        .starts_with("opencode native: invalid wire payload: session.execution.started data: "));
}

#[test]
fn a_lone_surrogate_escape_decodes_as_the_replacement_character_as_go_s_decoder_reads_it() {
    let event = decode_event(
        r#"{"id":"evt_5","type":"session.text.delta","data":{"sessionID":"ses_1","delta":"\ud800"}}"#,
    )
    .unwrap();
    assert_eq!(event.session_id, "ses_1");
}
