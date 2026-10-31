mod support;

use oap_protocol::{
    Envelope, EnvelopeType, Message, MessageContent, MessageRole, OpenMessage,
    RequestedDeliveryMode, SessionOpenRequest, ToolSourceAttachment,
};
use oap_serve::stdio::{serve, StdioOptions};
use oap_serve::{Hub, Registry};
use serde_json::Value;

fn request(payload: &SessionOpenRequest, revision: Option<&str>) -> String {
    let mut envelope =
        Envelope::new(EnvelopeType::SESSION_OPEN_REQUEST, "req-open", payload).unwrap();
    envelope.capability_revision = revision.map(Into::into);
    serde_json::to_string(&envelope).unwrap()
}

fn opening(id: &str) -> SessionOpenRequest {
    SessionOpenRequest {
        session_id: Some(id.into()),
        ..SessionOpenRequest::default()
    }
}

fn lines(hub: &Hub, input: &[String]) -> Vec<Value> {
    lines_within(hub, input, &StdioOptions::default())
}

fn lines_within(hub: &Hub, input: &[String], options: &StdioOptions) -> Vec<Value> {
    let input: String = input.iter().map(|line| format!("{line}\n")).collect();
    let output = support::Captured::default();
    serve(
        hub,
        std::io::Cursor::new(input.to_owned().into_bytes()),
        output.clone(),
        options,
    )
    .unwrap();
    String::from_utf8(output.bytes())
        .unwrap()
        .lines()
        .map(|line| serde_json::from_str(line).unwrap())
        .collect()
}

fn open_line(id: i64, adapter: &str, request: &str) -> String {
    format!(r#"{{"id":{id},"op":"open","adapter":"{adapter}","request":{request}}}"#)
}

#[test]
fn open_answers_a_session_open_response_citing_its_request() {
    let hub = Hub::new(Registry::default_registry());
    let answered = lines(
        &hub,
        &[open_line(1, "memory", &request(&opening("s"), None))],
    );
    assert_eq!(answered[0]["ok"], true, "{}", answered[0]);
    let response = &answered[0]["result"];
    assert_eq!(response["type"], "session.open.response");
    assert_eq!(response["in_reply_to"], "req-open");
    assert_eq!(response["session_id"], "s");
    assert_eq!(response["payload"]["session_id"], "s");
    assert_eq!(response["payload"]["status"], "idle");
    assert!(response.get("capability_revision").is_none());
    assert!(hub.session(&"s".into()).is_ok());
}

#[test]
fn open_needs_an_adapter_it_knows_and_an_id_not_already_live() {
    let hub = Hub::new(Registry::default_registry());
    let open = request(&opening("s"), None);
    let answered = lines(
        &hub,
        &[
            r#"{"id":1,"op":"open","request":{}}"#.to_owned(),
            open_line(2, "ghost", &open),
            open_line(3, "memory", &open),
            open_line(4, "memory", &open),
            r#"{"id":5,"op":"open","adapter":"memory","session_id":"s"}"#.to_owned(),
        ],
    );
    assert_eq!(answered[0]["error"]["code"], "invalid_request");
    assert_eq!(answered[0]["error"]["message"], "adapter is required");
    assert_eq!(answered[1]["error"]["code"], "unknown_adapter");
    assert_eq!(answered[1]["error"]["message"], "no adapter \"ghost\"");
    assert_eq!(answered[2]["ok"], true);
    assert_eq!(answered[3]["error"]["code"], "session_exists");
    assert_eq!(answered[4]["error"]["code"], "invalid_request");
}

#[test]
fn an_open_citing_a_stale_revision_for_what_it_elects_is_refused_with_both_revisions() {
    let hub = Hub::new(Registry::default_registry());
    let subscribing = SessionOpenRequest {
        subscribe: true,
        ..opening("s")
    };
    let answered = lines(
        &hub,
        &[open_line(
            1,
            "memory",
            &request(&subscribing, Some("stale-rev")),
        )],
    );
    let error = &answered[0]["error"];
    assert_eq!(error["code"], "stale_capabilities");
    assert_eq!(error["details"]["current_revision"], "stale-rev");
    assert_eq!(
        error["details"]["expected_revision"],
        oap_adapter::memory::CAPABILITY_REVISION
    );
}

#[test]
fn an_open_attaching_an_unconfigured_process_source_is_refused_naming_it() {
    let hub = Hub::new(Registry::default_registry());
    let attaching = SessionOpenRequest {
        tool_sources: vec![ToolSourceAttachment {
            id: "fs".into(),
            kind: oap_protocol::TOOL_SOURCE_PROCESS.into(),
            ..ToolSourceAttachment::default()
        }],
        ..opening("s")
    };
    let answered = lines(&hub, &[open_line(1, "memory", &request(&attaching, None))]);
    let error = &answered[0]["error"];
    assert_eq!(error["code"], "unsupported_feature", "{error}");
    assert_eq!(error["details"]["source"], "fs");
    assert_eq!(error["details"]["feature"], "action.tool_sources.attach");
    assert_eq!(error["details"]["reason"], "unsatisfiable");
}

#[test]
fn a_subscribing_open_answers_and_then_streams_on_its_own_id() {
    let hub = Hub::new(Registry::default_registry());
    let subscribing = SessionOpenRequest {
        subscribe: true,
        ..opening("s")
    };
    let answered = lines(
        &hub,
        &[
            open_line(1, "memory", &request(&subscribing, None)),
            r#"{"id":2,"op":"close","session_id":"s"}"#.to_owned(),
        ],
    );
    assert_eq!(answered[0]["id"], 1);
    assert_eq!(answered[0]["result"]["type"], "session.open.response");
    assert_eq!(
        answered[0]["result"]["capability_revision"],
        oap_adapter::memory::CAPABILITY_REVISION
    );
    let closed: Vec<&Value> = answered
        .iter()
        .filter(|line| line["event"] == "oap-session-closed")
        .collect();
    assert_eq!(closed.len(), 1);
    assert_eq!(closed[0]["id"], 1);
}

#[test]
fn a_subscribing_open_past_the_subscription_cap_is_busy_and_opens_nothing() {
    let hub = Hub::new(Registry::default_registry());
    let subscribing = |id: &str| SessionOpenRequest {
        subscribe: true,
        ..opening(id)
    };
    let answered = lines_within(
        &hub,
        &[
            open_line(1, "memory", &request(&subscribing("s"), None)),
            open_line(2, "memory", &request(&subscribing("t"), None)),
        ],
        &StdioOptions {
            max_subscriptions: 1,
            ..StdioOptions::default()
        },
    );
    let refused = answered.iter().find(|line| line["id"] == 2).unwrap();
    assert_eq!(refused["error"]["code"], "busy", "{refused}");
    assert!(hub.session(&"t".into()).is_err());
}

#[test]
fn an_oversized_open_response_rolls_back_a_minted_session_and_keeps_a_named_one() {
    let options = StdioOptions {
        frame_limit: 300,
        ..StdioOptions::default()
    };
    let hub = Hub::new(Registry::default_registry());
    let minted = lines_within(
        &hub,
        &[open_line(
            1,
            "memory",
            &request(&SessionOpenRequest::default(), None),
        )],
        &options,
    );
    assert_eq!(
        minted[0]["error"]["code"], "response_too_large",
        "{}",
        minted[0]
    );
    assert!(minted[0]["error"]["message"]
        .as_str()
        .unwrap()
        .ends_with("the session was rolled back"));
    assert!(hub.sessions().is_empty());

    let named = lines_within(
        &hub,
        &[open_line(1, "memory", &request(&opening("s"), None))],
        &options,
    );
    assert_eq!(named[0]["error"]["code"], "response_too_large");
    assert!(named[0]["error"]["message"]
        .as_str()
        .unwrap()
        .ends_with("the session is open under the session_id the request supplied"));
    assert!(hub.session(&"s".into()).is_ok());
}

fn carrying(delivery: RequestedDeliveryMode) -> SessionOpenRequest {
    SessionOpenRequest {
        message: Some(OpenMessage {
            delivery,
            messages: vec![Message {
                role: MessageRole::USER,
                content: MessageContent::from_text("run"),
                ..Message::default()
            }],
            ..OpenMessage::default()
        }),
        ..opening("s")
    }
}

#[test]
fn an_open_carrying_a_message_reports_the_run_it_admitted_in_its_state() {
    let hub = Hub::new(Registry::default_registry());
    let answered = lines(
        &hub,
        &[open_line(
            1,
            "memory",
            &request(&carrying(RequestedDeliveryMode::AUTO), None),
        )],
    );
    let state = &answered[0]["result"]["payload"];
    assert_eq!(state["status"], "running", "{}", answered[0]);
    let run = &state["active_run_id"];
    assert!(run.is_string());
    let active = &state["active_runs"][0];
    assert_eq!(&active["run_id"], run);
    assert_eq!(active["relationship"], "primary");
    assert_eq!(
        active["admitted_submit_requests"],
        serde_json::json!(["req-open"])
    );
}

#[test]
fn an_open_whose_message_is_refused_rolls_its_session_back() {
    let hub = Hub::new(Registry::default_registry());
    let answered = lines(
        &hub,
        &[open_line(
            1,
            "memory",
            &request(&carrying(RequestedDeliveryMode::STEER), None),
        )],
    );
    assert_eq!(
        answered[0]["error"]["code"], "invalid_steer_target",
        "{}",
        answered[0]
    );
    assert!(hub.session(&"s".into()).is_err());
    assert!(hub.sessions().is_empty());
}

#[test]
fn an_oversized_open_response_rolls_back_a_named_session_whose_message_it_admitted() {
    let hub = Hub::new(Registry::default_registry());
    let line = open_line(
        1,
        "memory",
        &request(&carrying(RequestedDeliveryMode::AUTO), None),
    );
    let answered = lines_within(
        &hub,
        std::slice::from_ref(&line),
        &StdioOptions {
            frame_limit: line.len() + 8,
            ..StdioOptions::default()
        },
    );
    assert_eq!(
        answered[0]["error"]["code"], "response_too_large",
        "{}",
        answered[0]
    );
    assert!(
        answered[0]["error"]["message"]
            .as_str()
            .unwrap()
            .ends_with("the session was rolled back"),
        "{}",
        answered[0]
    );
    assert!(hub.session(&"s".into()).is_err());
}
