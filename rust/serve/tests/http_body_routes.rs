use std::io::{Read, Write};
use std::net::{SocketAddr, TcpListener, TcpStream};

use futures::executor::block_on;
use oap_protocol::{
    Envelope, EnvelopeType, Message, MessageContent, MessageRole, MessageSubmitRequest,
    RequestedDeliveryMode, RunCancelRequest,
};
use oap_serve::http::{serve_http, HttpOptions};
use oap_serve::{Hub, Registry};
use serde_json::Value;

fn started() -> (Hub, SocketAddr) {
    let hub = Hub::new(Registry::default_registry());
    block_on(hub.open(
        "memory",
        oap_adapter::OpenRequest {
            session_id: "s".into(),
            ..oap_adapter::OpenRequest::default()
        },
    ))
    .unwrap();
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let addr = listener.local_addr().unwrap();
    let served = hub.clone();
    std::thread::spawn(move || serve_http(&served, listener, &HttpOptions::default()));
    (hub, addr)
}

struct Reply {
    status: u16,
    headers: String,
    body: Vec<u8>,
}

impl Reply {
    fn json(&self) -> Value {
        serde_json::from_slice(&self.body).unwrap()
    }
}

fn send(addr: SocketAddr, raw: &[u8]) -> Reply {
    let mut stream = TcpStream::connect(addr).unwrap();
    stream
        .set_read_timeout(Some(std::time::Duration::from_secs(10)))
        .unwrap();
    stream.write_all(raw).unwrap();
    let mut out = Vec::new();
    stream.read_to_end(&mut out).unwrap();
    let split = out
        .windows(4)
        .position(|window| window == b"\r\n\r\n")
        .unwrap();
    let headers = String::from_utf8(out[..split].to_vec()).unwrap();
    Reply {
        status: headers[9..12].parse().unwrap(),
        headers,
        body: out[split + 4..].to_vec(),
    }
}

fn post(addr: SocketAddr, path: &str, media: &str, body: &str) -> Reply {
    send(
        addr,
        format!(
            "POST {path} HTTP/1.1\r\nHost: {addr}\r\nContent-Type: {media}\r\nContent-Length: {}\r\n\r\n{body}",
            body.len()
        )
        .as_bytes(),
    )
}

fn submit(session: &str) -> String {
    let mut envelope = Envelope::new(
        EnvelopeType::SESSION_MESSAGE_SUBMIT_REQUEST,
        "submit-1",
        &MessageSubmitRequest {
            session_id: session.into(),
            delivery: RequestedDeliveryMode::AUTO,
            messages: vec![Message {
                role: MessageRole::USER,
                content: MessageContent::from_text("run"),
                ..Message::default()
            }],
            ..MessageSubmitRequest::default()
        },
    )
    .unwrap();
    envelope.session_id = Some(session.into());
    serde_json::to_string(&envelope).unwrap()
}

#[test]
fn a_submit_answers_its_admission_and_a_cancel_and_close_settle_the_session() {
    let (_, addr) = started();
    let admitted = post(addr, "/sessions/s/submit", "application/json", &submit("s"));
    assert_eq!(
        admitted.status,
        200,
        "{}",
        String::from_utf8_lossy(&admitted.body)
    );
    let response = admitted.json();
    assert_eq!(response["type"], "session.message.submit.response");
    assert_eq!(response["in_reply_to"], "submit-1");
    let run = response["run_id"].as_str().unwrap().to_owned();
    let busy = post(addr, "/sessions/s/close", "application/json", "");
    assert_eq!(busy.status, 409, "{}", String::from_utf8_lossy(&busy.body));
    assert_eq!(busy.json()["payload"]["error"]["code"], "run_active");
    let mut cancel = Envelope::new(
        EnvelopeType::RUN_CANCEL_REQUEST,
        "cancel-1",
        &RunCancelRequest {
            session_id: "s".into(),
            run_id: run.as_str().into(),
            ..RunCancelRequest::default()
        },
    )
    .unwrap();
    cancel.session_id = Some("s".into());
    cancel.run_id = Some(run.as_str().into());
    let cancelled = post(
        addr,
        "/sessions/s/cancel",
        "application/json; charset=utf-8",
        &serde_json::to_string(&cancel).unwrap(),
    );
    assert_eq!(
        cancelled.status,
        200,
        "{}",
        String::from_utf8_lossy(&cancelled.body)
    );
    assert_eq!(cancelled.json()["type"], "run.cancel.response");
    std::thread::sleep(std::time::Duration::from_millis(100));
    let closed = send(
        addr,
        format!("POST /sessions/s/close HTTP/1.1\r\nHost: {addr}\r\n\r\n").as_bytes(),
    );
    assert_eq!(
        closed.status,
        204,
        "{}",
        String::from_utf8_lossy(&closed.body)
    );
    assert!(closed.body.is_empty());
    assert!(!closed.headers.contains("Content-Length"));
    let again = post(addr, "/sessions/s/close", "application/json", "");
    assert_eq!(again.status, 404);
}

#[test]
fn a_body_route_gates_its_media_then_its_size_then_its_json() {
    let (_, addr) = started();
    let media = post(addr, "/sessions/s/submit", "text/plain", &submit("s"));
    assert_eq!(media.status, 415);
    assert_eq!(
        media.json()["payload"]["error"]["code"],
        "unsupported_media_type"
    );
    let large = send(
        addr,
        format!(
            "POST /sessions/s/submit HTTP/1.1\r\nHost: {addr}\r\nContent-Type: application/json\r\nContent-Length: 20000000\r\n\r\n"
        )
        .as_bytes(),
    );
    assert_eq!(large.status, 413);
    assert_eq!(
        large.json()["payload"]["error"]["code"],
        "request_too_large"
    );
    let malformed = post(addr, "/sessions/s/submit", "application/json", "{not json");
    assert_eq!(malformed.status, 400);
    assert_eq!(
        malformed.json()["payload"]["error"]["code"],
        "malformed_json"
    );
}

#[test]
fn a_refusal_after_the_body_is_read_replies_to_the_request_envelope() {
    let (_, addr) = started();
    let mismatched = post(addr, "/sessions/s/cancel", "application/json", &submit("s"));
    assert_eq!(mismatched.status, 400);
    let envelope = mismatched.json();
    assert_eq!(envelope["payload"]["error"]["code"], "type_mismatch");
    assert_eq!(envelope["in_reply_to"], "submit-1");
    let unknown = post(
        addr,
        "/sessions/ghost/submit",
        "application/json",
        &submit("ghost"),
    );
    assert_eq!(unknown.status, 404);
    let envelope = unknown.json();
    assert_eq!(envelope["payload"]["error"]["code"], "unknown_session");
    assert_eq!(envelope["in_reply_to"], "submit-1");
    assert_eq!(envelope["session_id"], "ghost");
}
