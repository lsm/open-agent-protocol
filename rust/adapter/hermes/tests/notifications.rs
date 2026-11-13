use oap_adapter_hermes::native::{decode_notification, is_run_scoped, OBSERVED_EVENTS};

const GO_DECODED: &[(&str, Option<&str>)] = &[
    (
        r#"{"type":"gateway.ready","payload":{"skin":{},"change_events":true,"replay_epoch":"zzzz456789abcdef0123456789abcdef"}}"#,
        Some(r#"hermes native: invalid pinned message: invalid gateway.ready payload"#),
    ),
    (
        r#"{"type":"gateway.ready","payload":{"skin":{},"change_events":true,"replay_epoch":"g123456789abcdef0123456789abcdef"}}"#,
        Some(r#"hermes native: invalid pinned message: invalid gateway.ready payload"#),
    ),
    (
        r#"{"type":"message.interim","session_id":"s","seq":1,"payload":{"text":"x","x":1}}"#,
        Some(
            r#"hermes native: invalid pinned message: invalid message.interim payload: json: unknown field "x""#,
        ),
    ),
    (
        r#"{"type":"message.interim","session_id":"s","seq":1}"#,
        Some(r#"hermes native: invalid pinned message: invalid message.interim payload: EOF"#),
    ),
    (
        r#"{"type":"session.usage","session_id":"s","seq":1,"payload":{"usage":[1]}}"#,
        Some(
            r#"hermes native: invalid pinned message: invalid session.usage payload: json: cannot unmarshal array into Go value of type native.usageAlias"#,
        ),
    ),
    (
        r#"{"type":"session.usage","session_id":"s","seq":1}"#,
        Some(r#"hermes native: invalid pinned message: invalid session.usage payload: EOF"#),
    ),
    (
        r#"{"type":"gateway.ready","payload":{"skin":{"name":"default"},"change_events":true,"replay_epoch":"0123456789abcdef0123456789abcdef"}}"#,
        None,
    ),
    (
        r#"{"type":"gateway.ready","payload":{"skin":{},"change_events":true,"replay_epoch":"abcd"}}"#,
        Some(r#"hermes native: invalid pinned message: invalid gateway.ready payload"#),
    ),
    (
        r#"{"type":"gateway.ready","payload":{"skin":{},"change_events":true,"replay_epoch":"0123456789ABCDEF0123456789abcdef"}}"#,
        Some(r#"hermes native: invalid pinned message: invalid gateway.ready payload"#),
    ),
    (
        r#"{"type":"gateway.ready","payload":{"skin":{},"change_events":false,"replay_epoch":"0123456789abcdef0123456789abcdef"}}"#,
        Some(r#"hermes native: invalid pinned message: invalid gateway.ready payload"#),
    ),
    (
        r#"{"type":"gateway.ready","payload":{"change_events":true,"replay_epoch":"0123456789abcdef0123456789abcdef"}}"#,
        Some(r#"hermes native: invalid pinned message: invalid gateway.ready payload"#),
    ),
    (
        r#"{"type":"gateway.ready","payload":{"skin":null,"change_events":true,"replay_epoch":"0123456789abcdef0123456789abcdef"}}"#,
        None,
    ),
    (
        r#"{"session_id":"abcd1234","seq":1,"type":"gateway.ready","payload":{"skin":{"name":"default"},"change_events":true,"replay_epoch":"0123456789abcdef0123456789abcdef"}}"#,
        Some(r#"hermes native: invalid pinned message: gateway.ready must be session-less"#),
    ),
    (
        r#"{"type":"gateway.ready"}"#,
        Some(r#"hermes native: invalid pinned message: invalid gateway.ready payload: EOF"#),
    ),
    (
        r#"{"type":"gateway.ready","payload":{"skin":{},"change_events":true,"replay_epoch":"0123456789abcdef0123456789abcdef","x":1}}"#,
        Some(
            r#"hermes native: invalid pinned message: invalid gateway.ready payload: json: unknown field "x""#,
        ),
    ),
    (
        r#"{"type":"message.start","session_id":"abcd1234"}"#,
        Some(
            r#"hermes native: invalid pinned message: session event "message.start" requires a positive seq"#,
        ),
    ),
    (
        r#"{"type":"message.start","session_id":"abcd1234","seq":0}"#,
        Some(
            r#"hermes native: invalid pinned message: session event "message.start" requires a positive seq"#,
        ),
    ),
    (
        r#"{"type":"message.start","session_id":"abcd1234","seq":-1}"#,
        Some(
            r#"hermes native: invalid pinned message: session event "message.start" requires a positive seq"#,
        ),
    ),
    (
        r#"{"type":"skin.changed","payload":{},"seq":1}"#,
        Some(
            r#"hermes native: invalid pinned message: session-less event "skin.changed" must not carry seq"#,
        ),
    ),
    (
        r#"{"type":"message.start","session_id":"abcd1234","seq":1,"payload":{"text":"x","undeclared":true}}"#,
        Some(
            r#"hermes native: invalid pinned message: invalid message.start payload: json: unknown field "undeclared""#,
        ),
    ),
    (
        r#"{"type":"message.start","session_id":"abcd1234","seq":1}"#,
        None,
    ),
    (
        r#"{"type":"message.start","session_id":"s","seq":2,"payload":{"display_kind":"background","display_text":"done","notification_category":"task","text":"card","turn_id":"t1"}}"#,
        None,
    ),
    (
        r#"{"type":"message.start","session_id":"s","seq":2,"payload":null}"#,
        None,
    ),
    (
        r#"{"type":"message.start","session_id":"s","seq":2,"payload":[1]}"#,
        Some(
            r#"hermes native: invalid pinned message: invalid message.start payload: json: cannot unmarshal array into Go value of type native.MessageStartPayload"#,
        ),
    ),
    (
        r#"{"type":"message.complete","session_id":"s","seq":3,"payload":{"text":"x","status":"error"}}"#,
        Some(
            r#"hermes native: invalid pinned message: error settlement requires an error message"#,
        ),
    ),
    (
        r#"{"type":"message.complete","session_id":"s","seq":3,"payload":{"text":"x","status":"weird"}}"#,
        Some(r#"hermes native: invalid pinned message: message.complete status "weird""#),
    ),
    (
        r#"{"type":"message.complete","session_id":"s","seq":3,"payload":{"text":"x","status":"error","error":"boom"}}"#,
        None,
    ),
    (
        r#"{"type":"message.complete","session_id":"s","seq":3,"payload":{"text":"mirror"}}"#,
        None,
    ),
    (
        r#"{"type":"message.complete","session_id":"s","seq":3}"#,
        Some(r#"hermes native: invalid pinned message: invalid message.complete payload: EOF"#),
    ),
    (
        r#"{"type":"message.delta","session_id":"s","seq":1,"payload":{"text":"hi","rendered":"x"}}"#,
        None,
    ),
    (
        r#"{"type":"reasoning.delta","session_id":"s","seq":1,"payload":{"text":"hmm","rendered":"x"}}"#,
        Some(r#"hermes native: invalid pinned message: rendered is message.delta-only"#),
    ),
    (
        r#"{"type":"thinking.delta","session_id":"s","seq":1,"payload":{"text":"hmm","verbose":true}}"#,
        Some(r#"hermes native: invalid pinned message: verbose is reasoning.delta-only"#),
    ),
    (
        r#"{"type":"reasoning.delta","session_id":"s","seq":1,"payload":{"text":"hmm","verbose":true}}"#,
        None,
    ),
    (
        r#"{"type":"message.delta","session_id":"s","seq":1,"payload":{"text":"hmm","verbose":true}}"#,
        Some(r#"hermes native: invalid pinned message: verbose is reasoning.delta-only"#),
    ),
    (
        r#"{"type":"request.cancel","session_id":"s","seq":10,"payload":{"id":"srq-1","method":"clarify","reason":"interrupted"}}"#,
        None,
    ),
    (
        r#"{"type":"request.cancel","session_id":"s","seq":10,"payload":{"method":"clarify","reason":"timeout"}}"#,
        Some(r#"hermes native: invalid pinned message: request.cancel requires id and method"#),
    ),
    (
        r#"{"type":"request.cancel","session_id":"s","seq":10,"payload":{"id":"x"}}"#,
        Some(r#"hermes native: invalid pinned message: request.cancel requires id and method"#),
    ),
    (
        r#"{"type":"approval.request","session_id":"s","seq":2,"payload":{}}"#,
        Some(r#"hermes native: invalid pinned message: unknown event type "approval.request""#),
    ),
    (
        r#"{"type":"error","session_id":"s","seq":2,"payload":{"message":""}}"#,
        Some(r#"hermes native: invalid pinned message: error event requires a message"#),
    ),
    (
        r#"{"type":"error","session_id":"s","seq":2,"payload":{"message":"boom"}}"#,
        None,
    ),
    (r#"{"type":"error","payload":{"message":"boom"}}"#, None),
    (
        r#"{"type":"btw.complete","session_id":"s","seq":2,"payload":{"text":"t"}}"#,
        Some(r#"hermes native: invalid pinned message: task completion requires task_id"#),
    ),
    (
        r#"{"type":"background.complete","session_id":"s","seq":2,"payload":{"task_id":"k","text":"t"}}"#,
        None,
    ),
    (
        r#"{"type":"subagent.complete","session_id":"s","seq":7,"payload":{"goal":"g","task_count":1,"task_index":0,"cost_usd":0.5}}"#,
        Some(
            r#"hermes native: invalid pinned message: invalid subagent.complete payload: json: unknown field "cost_usd""#,
        ),
    ),
    (
        r#"{"type":"session.info","session_id":"s","seq":2}"#,
        Some(
            r#"hermes native: invalid pinned message: invalid session.info payload: unexpected end of JSON input"#,
        ),
    ),
    (
        r#"{"type":"session.info","session_id":"s","seq":2,"payload":{"model":"m","extra":1}}"#,
        None,
    ),
    (
        r#"{"type":"session.info","session_id":"s","seq":2,"payload":[1]}"#,
        Some(
            r#"hermes native: invalid pinned message: invalid session.info payload: json: cannot unmarshal array into Go value of type native.SessionInfoPayload"#,
        ),
    ),
    (
        r#"{"type":"session.info","session_id":"s","seq":2,"payload":null}"#,
        None,
    ),
    (
        r#"{"type":"skin.changed","session_id":"s","seq":4,"payload":{"anything":true}}"#,
        None,
    ),
    (
        r#"{"type":"approval.cancelled","payload":{"session_id":"s"}}"#,
        None,
    ),
    (
        r#"{"type":"future/unknown","session_id":"s","seq":4,"payload":{}}"#,
        Some(r#"hermes native: invalid pinned message: unknown event type "future/unknown""#),
    ),
    (
        r#"{"type":"","session_id":"s","seq":4}"#,
        Some(r#"hermes native: invalid pinned message: event type is required"#),
    ),
    (
        r#"{"session_id":"s","seq":4}"#,
        Some(r#"hermes native: invalid pinned message: event type is required"#),
    ),
    (
        r#"{"type":"message.start","session_id":"s","seq":1,"extra":1}"#,
        Some(
            r#"hermes native: invalid pinned message: invalid event envelope: json: unknown field "extra""#,
        ),
    ),
    (
        r#"[1]"#,
        Some(
            r#"hermes native: invalid pinned message: invalid event envelope: json: cannot unmarshal array into Go value of type native.Event"#,
        ),
    ),
    (
        r#"null"#,
        Some(r#"hermes native: invalid pinned message: event type is required"#),
    ),
    (
        r#""#,
        Some(r#"hermes native: invalid pinned message: invalid event envelope: EOF"#),
    ),
    (
        r#"{"type":"tool.start","session_id":"s","seq":1,"payload":{"tool_id":"t","name":"n","args":[1]}}"#,
        Some(
            r#"hermes native: invalid pinned message: invalid tool.start payload: json: cannot unmarshal array into Go struct field ToolStartPayload.args of type map[string]interface {}"#,
        ),
    ),
    (
        r#"{"type":"session.usage","session_id":"s","seq":1,"payload":{"usage":null}}"#,
        None,
    ),
    (
        r#"{"type":"message.interim","session_id":"s","seq":1,"payload":{"text":"x","already_streamed":true}}"#,
        None,
    ),
    (
        r#"{"type":"tool.complete","session_id":"s","seq":1}"#,
        Some(r#"hermes native: invalid pinned message: invalid tool.complete payload: EOF"#),
    ),
    (
        r#"{"type":"subagent.complete","session_id":"s","seq":1}"#,
        Some(r#"hermes native: invalid pinned message: invalid subagent.complete payload: EOF"#),
    ),
    (
        r#"{"type":"gateway.ready","session_id":"","seq":0,"payload":{"skin":{"name":"default"},"change_events":true,"replay_epoch":"0123456789abcdef0123456789abcdef"}}"#,
        None,
    ),
];

#[test]
fn each_event_go_decodes_or_refuses_is_decoded_or_refused_with_go_s_text() {
    let differ: Vec<_> = GO_DECODED
        .iter()
        .filter(|(frame, want)| {
            decode_notification("event", frame.as_bytes())
                .err()
                .as_deref()
                != *want
        })
        .map(|(frame, want)| (frame, want, decode_notification("event", frame.as_bytes())))
        .collect();
    assert!(differ.is_empty(), "{differ:#?}");
}

#[test]
fn a_mistyped_member_or_a_trailing_value_is_refused_under_go_s_prefix() {
    for (frame, prefix) in [
        (
            r#"{"type":"message.delta","session_id":"s","seq":1,"payload":{"text":5}}"#,
            "hermes native: invalid pinned message: invalid message.delta payload: ",
        ),
        (
            r#"{"type":"session.info","session_id":"s","seq":2,"payload":{"model":5}}"#,
            "hermes native: invalid pinned message: invalid session.info payload: ",
        ),
        (
            r#"{"type":"message.start","session_id":"s","seq":1} {}"#,
            "hermes native: invalid pinned message: invalid event envelope: ",
        ),
    ] {
        let refused = decode_notification("event", frame.as_bytes()).unwrap_err();
        assert!(refused.starts_with(prefix), "{frame}: {refused}");
    }
}

#[test]
fn a_notification_other_than_event_is_refused() {
    assert_eq!(
        decode_notification("something.else", b"{}").unwrap_err(),
        "hermes native: invalid pinned message: unknown notification method \"something.else\""
    );
}

#[test]
fn a_decoded_event_keeps_its_envelope() {
    let event = decode_notification(
        "event",
        br#"{"type":"message.delta","session_id":"s","seq":4,"payload":{"text":"hi"}}"#,
    )
    .unwrap();
    assert_eq!(
        (event.kind.as_str(), event.session_id.as_str(), event.seq),
        ("message.delta", "s", 4)
    );
    assert_eq!(event.payload_bytes(), br#"{"text":"hi"}"#);
}

#[test]
fn every_event_go_admits_as_observed_is_admitted_with_any_payload() {
    assert_eq!(OBSERVED_EVENTS.len(), 65);
    for observed in OBSERVED_EVENTS {
        let frame = format!(
            r#"{{"type":"{observed}","session_id":"s","seq":4,"payload":{{"anything":true}}}}"#
        );
        assert!(
            decode_notification("event", frame.as_bytes()).is_ok(),
            "{observed}"
        );
    }
}

#[test]
fn the_run_scoped_events_are_go_s_seven() {
    let scoped: Vec<_> = [
        "gateway.ready",
        "message.start",
        "message.delta",
        "reasoning.delta",
        "thinking.delta",
        "message.interim",
        "message.complete",
        "error",
        "tool.start",
        "tool.complete",
        "session.usage",
        "session.info",
        "request.cancel",
        "subagent.complete",
        "btw.complete",
        "background.complete",
        "skin.changed",
    ]
    .into_iter()
    .filter(|kind| is_run_scoped(kind))
    .collect();
    assert_eq!(
        scoped,
        [
            "message.start",
            "message.delta",
            "reasoning.delta",
            "thinking.delta",
            "message.complete",
            "tool.start",
            "tool.complete"
        ]
    );
}
