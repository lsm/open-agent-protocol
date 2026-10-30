use oap_adapter_deepseek::native::{decode_strict, Event};

fn check(kind: &str, data: &str) -> Result<(), String> {
    check_event(&format!(
        r#"{{"type":"{kind}","seq":1,"time":1,"data":{data}}}"#
    ))
}

fn check_event(event: &str) -> Result<(), String> {
    decode_strict::<Event>(event.as_bytes()).unwrap().validate()
}

fn refused(detail: &str) -> Result<(), String> {
    Err(format!("deepseek native: invalid pinned message: {detail}"))
}

const TEXT: &str = r#"[{"type":"text","text":"hi"}]"#;

#[test]
fn the_envelope_needs_a_type_data_and_non_negative_counters() {
    assert_eq!(
        check_event(r#"{"type":"turn/start","seq":1,"time":1}"#),
        refused("invalid event envelope")
    );
    assert_eq!(
        check_event(r#"{"type":"turn/start","seq":-1,"time":1,"data":{"turn":1}}"#),
        refused("invalid event envelope")
    );
    assert_eq!(
        check_event(
            r#"{"type":"turn/start","seq":1,"time":1,"data":{"turn":1},"ignorable":false}"#
        ),
        refused("invalid event envelope")
    );
    assert_eq!(check("compaction/start", "null"), Ok(()));
}

#[test]
fn surface_metadata_rides_only_surface_events() {
    assert_eq!(
        check_event(
            r#"{"type":"turn/start","seq":1,"time":1,"data":{"turn":1},"sourceEventSeqs":[]}"#
        ),
        refused("surface metadata on non-surface event")
    );
    let user = format!(r#"{{"id":"u","role":"user","content":{TEXT},"source":{{"kind":"user"}}}}"#);
    for (op, outcome) in [
        (r#""append""#, Ok(())),
        (r#"{"op":"replace","start":1,"end":1}"#, Ok(())),
        (
            r#"{"op":"replace","start":2,"end":1}"#,
            refused("invalid surfaceOp"),
        ),
        ("null", refused("invalid surfaceOp")),
    ] {
        let event = format!(
            r#"{{"type":"user/message","seq":1,"time":1,"data":{user},"surfaceOp":{op},"sourceEventSeqs":[0]}}"#
        );
        assert_eq!(check_event(&event), outcome, "{op}");
    }
}

#[test]
fn an_unknown_event_is_refused_unless_it_is_ignorable() {
    assert_eq!(
        check("future/event", "{}"),
        refused("unknown required event \"future/event\"")
    );
    assert_eq!(
        check_event(r#"{"type":"future/event","seq":1,"time":1,"data":{},"ignorable":true}"#),
        Ok(())
    );
}

#[test]
fn turn_and_step_boundaries_need_positive_counters_and_a_known_end() {
    assert_eq!(check("turn/start", r#"{"turn":1}"#), Ok(()));
    assert_eq!(
        check("turn/start", r#"{"turn":0}"#),
        refused("invalid turn/start")
    );
    assert_eq!(
        check("turn/start", r#""x""#),
        refused(
            "turn/start data: json: cannot unmarshal string into Go value of type native.TurnStart"
        )
    );
    for (reason, outcome) in [
        (r#"{"kind":"completed"}"#, Ok(())),
        (
            r#"{"kind":"aborted","reason":{"kind":"hook","reason":"h"}}"#,
            Ok(()),
        ),
        (
            r#"{"kind":"aborted","reason":{"kind":"user","reason":"u"}}"#,
            refused("invalid turn/end"),
        ),
        (
            r#"{"kind":"error","error":{"message":"m","code":"c"}}"#,
            Ok(()),
        ),
        (
            r#"{"kind":"completed","reason":null}"#,
            refused("invalid turn/end"),
        ),
        ("null", refused("invalid turn/end")),
    ] {
        assert_eq!(
            check("turn/end", &format!(r#"{{"turn":1,"reason":{reason}}}"#)),
            outcome,
            "{reason}"
        );
    }
    assert_eq!(check("step/end", r#"{"turn":1,"step":1}"#), Ok(()));
    assert_eq!(
        check("step/start", r#"{"turn":1}"#),
        refused("invalid step boundary")
    );
}

#[test]
fn user_messages_and_splices_need_user_sources_and_present_content() {
    let user = |content: &str, source: &str| {
        format!(r#"{{"id":"u","role":"user","content":{content},"source":{source}}}"#)
    };
    assert_eq!(
        check("user/message", &user(TEXT, r#"{"kind":"user"}"#)),
        Ok(())
    );
    assert_eq!(
        check("user/message", &user("[]", r#"{"kind":"inbox"}"#)),
        Ok(())
    );
    assert_eq!(
        check("user/message", &user("null", r#"{"kind":"user"}"#)),
        refused("invalid user/message")
    );
    assert_eq!(
        check(
            "user/message",
            &user(TEXT, r#"{"kind":"tool","callId":"c"}"#)
        ),
        refused("invalid user/message")
    );
    let splice = |inserted: &str| {
        format!(r#"{{"target":"next-turn","start":0,"inserted":{inserted},"outcome":"canceled"}}"#)
    };
    assert_eq!(check("agent/inbox/spliced", &splice("null")), Ok(()));
    assert_eq!(
        check(
            "agent/inbox/spliced",
            &splice(&format!("[{}]", user(TEXT, r#"{"kind":"user"}"#)))
        ),
        Ok(())
    );
    assert_eq!(
        check(
            "agent/inbox/spliced",
            &splice(&format!("[{}]", user("null", r#"{"kind":"user"}"#)))
        ),
        refused("invalid inserted user message content")
    );
    assert_eq!(
        check("agent/inbox/spliced", r#"{"target":"next-turn","start":0}"#),
        refused("invalid agent/inbox/spliced inserted")
    );
    assert_eq!(
        check("agent/inbox/spliced", "null"),
        refused("invalid agent/inbox/spliced")
    );
}

#[test]
fn assistant_and_tool_messages_need_their_sources_and_a_stream() {
    let assistant = format!(
        r#"{{"turn":1,"step":1,"message":{{"id":"a","role":"assistant","content":{TEXT},"source":{{"kind":"model","provider":"p","model":"m"}}}},"stream":[]}}"#
    );
    assert_eq!(check("assistant/message", &assistant), Ok(()));
    assert_eq!(
        check(
            "assistant/message",
            &assistant.replace(r#","stream":[]"#, "")
        ),
        refused("invalid assistant/message")
    );
    assert_eq!(
        check("assistant/attempt", r#"{"turn":1,"step":1,"stream":null}"#),
        Ok(())
    );
    let result = |call: &str, error: &str| {
        format!(
            r#"{{"turn":1,"step":1,"message":{{"id":"t","role":"tool","content":{TEXT},"source":{{"kind":"tool","callId":"c1"}},"toolCallId":"{call}"}},"error":{error}}}"#
        )
    };
    assert_eq!(check("tool/result", &result("c1", "null")), Ok(()));
    assert_eq!(
        check("tool/result", &result("c2", "null")),
        refused("invalid tool/result")
    );
    assert_eq!(
        check(
            "tool/result",
            &result("c1", r#"{"name":"n","code":"c","reason":{"x":1}}"#)
        ),
        refused("invalid tool/result")
    );
    assert_eq!(
        check(
            "tool/call",
            r#"{"turn":1,"step":1,"callId":"c","name":"n","arguments":"{}"}"#
        ),
        Ok(())
    );
    assert_eq!(
        check(
            "tool/call",
            r#"{"turn":1,"step":1,"callId":"c","name":"n","arguments":"{\"a\":1,\"a\":2}"}"#
        ),
        refused("invalid tool/call")
    );
}

#[test]
fn todos_requests_and_the_end_seed_follow_the_pin() {
    assert_eq!(
        check(
            "todo/write",
            r#"{"todos":[{"content":"c","status":"pending"}]}"#
        ),
        Ok(())
    );
    assert_eq!(
        check("todo/write", r#"{"todos":null}"#),
        refused("invalid todo/write")
    );
    assert_eq!(
        check(
            "todo/write",
            r#"{"todos":[{"content":"c","status":"done"}]}"#
        ),
        refused("invalid todo")
    );
    let header =
        |header: &str, extra: &str| format!(r#"{{"header":{header},"reason":"initial"{extra}}}"#);
    let config = r#"{"config":{"provider":"p","model":"m"},"tools":null}"#;
    assert_eq!(check("request/header", &header(config, "")), Ok(()));
    assert_eq!(
        check("request/header", &header(config, r#","startsSeries":null"#)),
        refused("invalid request/header")
    );
    assert_eq!(
        check(
            "request/header",
            &header(
                r#"{"config":{"provider":"p","model":"m"},"system":null}"#,
                ""
            )
        ),
        refused("invalid request/header")
    );
    assert_eq!(
        check(
            "request/header",
            &header(
                r#"{"config":{"provider":"p","model":"m"},"tools":[{"name":"n"}]}"#,
                ""
            )
        ),
        refused("invalid request/header")
    );
    assert_eq!(
        check(
            "request/context",
            r#"{"provider":"p","model":"m","contextWindow":1}"#
        ),
        Ok(())
    );
    assert_eq!(
        check(
            "request/context",
            r#"{"provider":"p","model":"m","contextWindow":0}"#
        ),
        refused("invalid request/context")
    );
    assert_eq!(check("session/end-seed", "{}"), Ok(()));
    assert_eq!(
        check("session/end-seed", "[]"),
        refused(
            "session/end-seed data: json: cannot unmarshal array into Go value of type struct {}"
        )
    );
}
