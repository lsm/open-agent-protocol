use oap_serve::endpoint::{Endpoint, EndpointOptions};
use oap_serve::{Hub, Registry};
use serde_json::{json, Value};

fn run(hub: &Hub, lines: &[Value]) -> Vec<Value> {
    let endpoint = Endpoint::new(
        hub,
        EndpointOptions {
            adapter: "memory".into(),
            ..EndpointOptions::default()
        },
    )
    .unwrap();
    let input: Vec<String> = lines.iter().map(Value::to_string).collect();
    let mut out = Vec::new();
    endpoint.run(input.join("\n").as_bytes(), &mut out).unwrap();
    String::from_utf8(out)
        .unwrap()
        .lines()
        .map(|line| serde_json::from_str(line).unwrap())
        .collect()
}

fn request(kind: &str, id: &str, payload: Value) -> Value {
    json!({
        "protocol": "oap",
        "version": "0.1",
        "profile": "open-agent-protocol.agent-control-core",
        "type": kind,
        "id": id,
        "payload": payload,
    })
}

fn error(answer: &Value) -> &Value {
    &answer["payload"]["error"]
}

#[test]
fn an_open_answers_the_sessions_state_citing_the_request_and_its_revision() {
    let hub = Hub::new(Registry::default_registry());
    let mut open = request("session.open.request", "o", json!({"session_id": "s"}));
    open["capability_revision"] = oap_adapter::memory::CAPABILITY_REVISION.into();
    let answered = run(&hub, &[open.clone(), open]);
    let answer = &answered[0];
    assert_eq!(answer["type"], "session.open.response", "{answer}");
    assert_eq!(answer["in_reply_to"], "o");
    assert_eq!(answer["session_id"], "s");
    assert_eq!(answer["payload"]["session_id"], "s");
    assert_eq!(
        answer["capability_revision"],
        oap_adapter::memory::CAPABILITY_REVISION
    );
    assert!(hub.session(&"s".into()).is_ok());
    assert_eq!(error(&answered[1])["code"], "session_exists");
}

#[test]
fn an_open_carrying_a_message_is_refused_naming_the_field() {
    let hub = Hub::new(Registry::default_registry());
    let answered = run(
        &hub,
        &[request(
            "session.open.request",
            "o",
            json!({"session_id": "s", "message": {"delivery": "auto", "messages": [{"role": "user", "content": "hi"}]}}),
        )],
    );
    let refused = error(&answered[0]);
    assert_eq!(refused["code"], "unsupported_feature", "{refused}");
    assert_eq!(refused["details"]["feature"], "session.message.submit");
    assert_eq!(refused["details"]["field"], "message");
    assert!(hub.session(&"s".into()).is_err());
}

#[test]
fn an_open_runs_as_the_participant_initialize_named() {
    let tools = json!({"session_id": "s", "tools": [{"name": "t", "execution_owner": "host-1"}]});
    let unnamed = run(
        &Hub::new(Registry::default_registry()),
        &[request("session.open.request", "o", tools.clone())],
    );
    assert!(error(&unnamed[0])["message"]
        .as_str()
        .unwrap()
        .contains("execution_owner must be the opening participant"));
    let named = run(
        &Hub::new(Registry::default_registry()),
        &[
            request(
                "protocol.initialize.request",
                "i",
                json!({"participant": {"id": "host-1"}}),
            ),
            request("session.open.request", "o", tools),
        ],
    );
    let message = error(&named[1])["message"].as_str().unwrap().to_owned();
    assert!(
        !message.contains("execution_owner") && message.contains("must name a source"),
        "{message}"
    );
}

#[test]
fn a_state_request_answers_a_named_session_and_refuses_an_unnamed_or_unknown_one() {
    let hub = Hub::new(Registry::default_registry());
    let mut state = request("session.state.request", "q", json!({"session_id": "s"}));
    state["session_id"] = "s".into();
    let mut unknown = state.clone();
    unknown["session_id"] = "ghost".into();
    let answered = run(
        &hub,
        &[
            request("session.open.request", "o", json!({"session_id": "s"})),
            state,
            request("session.state.request", "u", json!({})),
            unknown,
        ],
    );
    assert_eq!(
        answered[1]["type"], "session.state.response",
        "{}",
        answered[1]
    );
    assert_eq!(answered[1]["session_id"], "s");
    assert_eq!(answered[1]["payload"]["status"], "idle");
    assert_eq!(error(&answered[2])["code"], "invalid_request");
    assert_eq!(
        error(&answered[2])["message"],
        "this request must name its session"
    );
    assert_eq!(error(&answered[3])["code"], "unknown_session");
    assert_eq!(answered[3]["session_id"], "ghost");
}
