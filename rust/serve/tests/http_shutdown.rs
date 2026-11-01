use std::io::{Read, Write};
use std::net::{SocketAddr, TcpListener, TcpStream};
use std::sync::mpsc::{Receiver, RecvTimeoutError};
use std::time::Duration;

use futures::executor::block_on;
use oap_adapter::{OpenRequest, SubmitRequest};
use oap_protocol::{
    Message, MessageContent, MessageRole, MessageSubmitRequest, RequestedDeliveryMode,
};
use oap_serve::http::{serve_http, HttpOptions};
use oap_serve::{Hub, Registry};
use serde_json::Value;

fn started() -> (Hub, SocketAddr, Receiver<()>) {
    let hub = Hub::new(Registry::default_registry());
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let addr = listener.local_addr().unwrap();
    let served = hub.clone();
    let (returned, returns) = std::sync::mpsc::channel();
    std::thread::spawn(move || {
        let _ = serve_http(&served, listener, &HttpOptions::default());
        let _ = returned.send(());
    });
    (hub, addr, returns)
}

fn running(hub: &Hub) -> oap_protocol::RunId {
    block_on(hub.open(
        "memory",
        OpenRequest {
            session_id: "s".into(),
            ..OpenRequest::default()
        },
    ))
    .unwrap();
    let admitted = block_on(hub.session(&"s".into()).unwrap().submit(SubmitRequest {
        request: MessageSubmitRequest {
            session_id: "s".into(),
            delivery: RequestedDeliveryMode::AUTO,
            messages: vec![Message {
                role: MessageRole::USER,
                content: MessageContent::from_text("run"),
                ..Message::default()
            }],
            ..MessageSubmitRequest::default()
        },
        ..SubmitRequest::default()
    }))
    .unwrap();
    admitted.run_id.unwrap()
}

fn post(addr: SocketAddr, path: &str, media: &str, body: &str) -> (u16, Value) {
    let mut stream = TcpStream::connect(addr).unwrap();
    stream
        .set_read_timeout(Some(Duration::from_secs(10)))
        .unwrap();
    stream
        .write_all(
            format!(
                "POST {path} HTTP/1.1\r\nHost: {addr}\r\nContent-Type: {media}\r\nContent-Length: {}\r\n\r\n{body}",
                body.len()
            )
            .as_bytes(),
        )
        .unwrap();
    let mut out = Vec::new();
    stream.read_to_end(&mut out).unwrap();
    let split = out
        .windows(4)
        .position(|window| window == b"\r\n\r\n")
        .unwrap();
    let status = String::from_utf8_lossy(&out[9..12]).parse().unwrap();
    (
        status,
        serde_json::from_slice(&out[split + 4..]).unwrap_or(Value::Null),
    )
}

fn code(answer: &(u16, Value)) -> (u16, Value) {
    (answer.0, answer.1["payload"]["error"]["code"].clone())
}

#[test]
fn a_shutdown_of_an_idle_hub_answers_stopping_and_the_server_returns() {
    let (_, addr, returns) = started();
    let (status, answer) = post(addr, "/shutdown", "application/json", "");
    assert_eq!(status, 200, "{answer}");
    assert_eq!(answer["outcome"], "stopping");
    returns
        .recv_timeout(Duration::from_secs(5))
        .expect("serve_http kept serving after it answered stopping");
}

#[test]
fn a_graceful_shutdown_drains_refusing_new_work_until_the_run_ends() {
    let (hub, addr, returns) = started();
    let run = running(&hub);
    let (status, chosen) = post(addr, "/shutdown", "application/json", "{}");
    assert_eq!((status, chosen["outcome"].clone()), (200, "choose".into()));
    assert_eq!(chosen["active_runs"][0]["run_id"], run.as_str());
    let (status, draining) = post(
        addr,
        "/shutdown",
        "application/json",
        r#"{"mode":"graceful"}"#,
    );
    assert_eq!(
        (status, draining["outcome"].clone()),
        (200, "draining".into())
    );
    assert_eq!(
        code(&post(
            addr,
            "/adapters/memory/sessions",
            "application/json",
            "{}"
        )),
        (503, "shutting_down".into())
    );
    assert_eq!(
        code(&post(addr, "/sessions/s/submit", "text/plain", "{}")),
        (415, "unsupported_media_type".into())
    );
    assert_eq!(
        returns.recv_timeout(Duration::from_millis(200)),
        Err(RecvTimeoutError::Timeout)
    );
    block_on(hub.session(&"s".into()).unwrap().cancel(run)).unwrap();
    returns
        .recv_timeout(Duration::from_secs(5))
        .expect("serve_http kept serving after the drain ended");
}

#[test]
fn a_shutdown_body_is_gated_by_media_then_json_then_its_members() {
    let (_, addr, _) = started();
    assert_eq!(
        code(&post(addr, "/shutdown", "text/plain", "")),
        (415, "unsupported_media_type".into())
    );
    assert_eq!(
        code(&post(addr, "/shutdown", "application/json", "[1]")),
        (400, "malformed_json".into())
    );
    assert_eq!(
        code(&post(
            addr,
            "/shutdown",
            "application/json",
            r#"{"mode":"later"}"#
        )),
        (400, "invalid_request".into())
    );
}
