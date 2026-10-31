use std::io::{BufRead, BufReader, Read, Write};
use std::net::{SocketAddr, TcpListener, TcpStream};
use std::time::Duration;

use oap_protocol::{
    Envelope, EnvelopeType, Message, MessageContent, MessageRole, OpenMessage,
    RequestedDeliveryMode, SessionOpenRequest,
};
use oap_serve::http::{serve_http, HttpOptions};
use oap_serve::{Hub, Registry};
use serde_json::Value;

fn started(options: HttpOptions) -> (Hub, SocketAddr) {
    let hub = Hub::new(Registry::default_registry());
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let addr = listener.local_addr().unwrap();
    let served = hub.clone();
    std::thread::spawn(move || serve_http(&served, listener, &options));
    (hub, addr)
}

fn opening(subscribe: bool, message: bool) -> SessionOpenRequest {
    SessionOpenRequest {
        session_id: Some("s".into()),
        subscribe,
        message: message.then(|| OpenMessage {
            delivery: RequestedDeliveryMode::AUTO,
            messages: vec![Message {
                role: MessageRole::USER,
                content: MessageContent::from_text("run"),
                ..Message::default()
            }],
            ..OpenMessage::default()
        }),
        ..SessionOpenRequest::default()
    }
}

fn body(payload: &SessionOpenRequest, revision: Option<&str>) -> String {
    let mut envelope =
        Envelope::new(EnvelopeType::SESSION_OPEN_REQUEST, "req-open", payload).unwrap();
    envelope.capability_revision = revision.map(Into::into);
    serde_json::to_string(&envelope).unwrap()
}

fn connect(addr: SocketAddr, raw: String) -> BufReader<TcpStream> {
    let mut stream = TcpStream::connect(addr).unwrap();
    stream
        .set_read_timeout(Some(Duration::from_secs(10)))
        .unwrap();
    stream.write_all(raw.as_bytes()).unwrap();
    BufReader::new(stream)
}

fn post(addr: SocketAddr, path: &str, body: &str) -> (u16, Value) {
    let mut reader = connect(
        addr,
        format!(
            "POST {path} HTTP/1.1\r\nHost: {addr}\r\nContent-Type: application/json\r\nContent-Length: {}\r\n\r\n{body}",
            body.len()
        ),
    );
    let status = head_status(&mut reader);
    let mut rest = Vec::new();
    reader.read_to_end(&mut rest).unwrap();
    (status, serde_json::from_slice(&rest).unwrap_or(Value::Null))
}

fn head_status(reader: &mut BufReader<TcpStream>) -> u16 {
    let mut head = String::new();
    loop {
        let mut line = String::new();
        reader.read_line(&mut line).unwrap();
        if line == "\r\n" || line.is_empty() {
            break;
        }
        head.push_str(&line);
    }
    head[9..12].parse().unwrap()
}

fn frames(addr: SocketAddr, path: &str) -> Vec<(Option<String>, Value)> {
    let mut reader = connect(addr, format!("GET {path} HTTP/1.1\r\nHost: {addr}\r\n\r\n"));
    assert_eq!(head_status(&mut reader), 200);
    reader
        .get_ref()
        .set_read_timeout(Some(Duration::from_millis(500)))
        .unwrap();
    let mut raw = Vec::new();
    let _ = reader.read_to_end(&mut raw);
    let text = String::from_utf8_lossy(&raw);
    text.split("\n\n")
        .filter(|frame| !frame.trim().is_empty())
        .map(|frame| {
            let event = frame
                .lines()
                .find_map(|line| line.strip_prefix("event: "))
                .map(str::to_owned);
            let data = frame
                .lines()
                .find_map(|line| line.strip_prefix("data: "))
                .map(|data| serde_json::from_str(data).unwrap())
                .unwrap_or(Value::Null);
            (event, data)
        })
        .collect()
}

fn saw_the_run_start(frames: &[(Option<String>, Value)]) -> bool {
    frames
        .iter()
        .any(|(event, data)| event.is_none() && data["type"] == "run.started")
}

#[test]
fn an_open_answers_its_response_and_refuses_an_unknown_adapter_or_a_live_id() {
    let (hub, addr) = started(HttpOptions::default());
    let (status, response) = post(
        addr,
        "/adapters/memory/sessions",
        &body(&opening(false, false), None),
    );
    assert_eq!(status, 200, "{response}");
    assert_eq!(response["type"], "session.open.response");
    assert_eq!(response["in_reply_to"], "req-open");
    assert_eq!(response["session_id"], "s");
    assert!(hub.session(&"s".into()).is_ok());
    let (status, refused) = post(
        addr,
        "/adapters/memory/sessions",
        &body(&opening(false, false), None),
    );
    assert_eq!(
        (status, refused["payload"]["error"]["code"].clone()),
        (409, "session_exists".into())
    );
    assert_eq!(refused["in_reply_to"], "req-open");
    let (status, refused) = post(
        addr,
        "/adapters/ghost/sessions",
        &body(&opening(false, false), None),
    );
    assert_eq!(
        (status, refused["payload"]["error"]["code"].clone()),
        (404, "unknown_adapter".into())
    );
}

#[test]
fn an_open_citing_a_stale_revision_is_a_409_naming_both_revisions() {
    let (_, addr) = started(HttpOptions::default());
    let (status, refused) = post(
        addr,
        "/adapters/memory/sessions",
        &body(&opening(true, false), Some("stale-rev")),
    );
    let error = &refused["payload"]["error"];
    assert_eq!(
        (status, error["code"].clone()),
        (409, "stale_capabilities".into()),
        "{refused}"
    );
    assert_eq!(error["details"]["current_revision"], "stale-rev");
}

#[test]
fn the_first_uncursored_stream_after_a_subscribing_open_carries_the_run_the_open_started() {
    let (_, addr) = started(HttpOptions::default());
    let (status, _) = post(
        addr,
        "/adapters/memory/sessions",
        &body(&opening(true, true), None),
    );
    assert_eq!(status, 200);
    std::thread::sleep(Duration::from_millis(100));
    let streamed = frames(addr, "/sessions/s/events");
    assert!(saw_the_run_start(&streamed), "{streamed:?}");
}

#[test]
fn a_held_subscription_expires_after_its_hold() {
    let (_, addr) = started(HttpOptions {
        subscription_hold: Some(Duration::from_millis(50)),
        ..HttpOptions::default()
    });
    let (status, _) = post(
        addr,
        "/adapters/memory/sessions",
        &body(&opening(true, true), None),
    );
    assert_eq!(status, 200);
    std::thread::sleep(Duration::from_millis(300));
    let streamed = frames(addr, "/sessions/s/events");
    assert!(!saw_the_run_start(&streamed), "{streamed:?}");
}

#[test]
fn a_cursored_stream_discards_the_held_subscription() {
    let (_, addr) = started(HttpOptions::default());
    let (status, _) = post(
        addr,
        "/adapters/memory/sessions",
        &body(&opening(true, true), None),
    );
    assert_eq!(status, 200);
    std::thread::sleep(Duration::from_millis(100));
    let mut replay = connect(
        addr,
        format!("GET /sessions/s/events?after=0 HTTP/1.1\r\nHost: {addr}\r\n\r\n"),
    );
    assert_eq!(head_status(&mut replay), 200);
    drop(replay);
    let streamed = frames(addr, "/sessions/s/events");
    assert!(!saw_the_run_start(&streamed), "{streamed:?}");
}
