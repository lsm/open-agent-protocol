use std::io::{BufRead, BufReader, Read, Write};
use std::net::{SocketAddr, TcpListener, TcpStream};
use std::time::Duration;

use futures::executor::block_on;
use oap_adapter::{OpenRequest, SubmitRequest};
use oap_protocol::{
    Message, MessageContent, MessageRole, MessageSubmitRequest, RequestedDeliveryMode,
};
use oap_serve::http::{serve_http, HttpOptions};
use oap_serve::{Hub, Registry};
use serde_json::Value;

fn started() -> (Hub, SocketAddr) {
    let hub = Hub::new(Registry::default_registry());
    block_on(hub.open(
        "memory",
        OpenRequest {
            session_id: "s".into(),
            ..OpenRequest::default()
        },
    ))
    .unwrap();
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let addr = listener.local_addr().unwrap();
    let served = hub.clone();
    std::thread::spawn(move || serve_http(&served, listener, &HttpOptions::default()));
    (hub, addr)
}

fn submit(hub: &Hub) {
    block_on(hub.session(&"s".into()).unwrap().submit(SubmitRequest {
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
}

fn open_stream(addr: SocketAddr, path: &str) -> BufReader<TcpStream> {
    let mut stream = TcpStream::connect(addr).unwrap();
    stream
        .set_read_timeout(Some(Duration::from_secs(10)))
        .unwrap();
    stream
        .write_all(format!("GET {path} HTTP/1.1\r\nHost: {addr}\r\n\r\n").as_bytes())
        .unwrap();
    BufReader::new(stream)
}

fn status_and_headers(reader: &mut BufReader<TcpStream>) -> (u16, String) {
    let mut head = String::new();
    loop {
        let mut line = String::new();
        reader.read_line(&mut line).unwrap();
        if line == "\r\n" || line.is_empty() {
            break;
        }
        head.push_str(&line);
    }
    (head[9..12].parse().unwrap(), head)
}

fn next_event(reader: &mut BufReader<TcpStream>) -> (Option<String>, Option<String>, Value) {
    let (mut id, mut event, mut data) = (None, None, Value::Null);
    loop {
        let mut line = String::new();
        if reader.read_line(&mut line).unwrap() == 0 {
            return (id, event, data);
        }
        let line = line.trim_end_matches(['\r', '\n']);
        if line.is_empty() {
            return (id, event, data);
        }
        if let Some(value) = line.strip_prefix("id: ") {
            id = Some(value.to_owned());
        } else if let Some(value) = line.strip_prefix("event: ") {
            event = Some(value.to_owned());
        } else if let Some(value) = line.strip_prefix("data: ") {
            data = serde_json::from_str(value).unwrap();
        }
    }
}

#[test]
fn a_subscriber_receives_the_runs_envelopes_as_sse_events_keyed_by_sequence() {
    let (hub, addr) = started();
    let mut reader = open_stream(addr, "/sessions/s/events");
    let (status, headers) = status_and_headers(&mut reader);
    assert_eq!(status, 200);
    assert!(
        headers.contains("Content-Type: text/event-stream"),
        "{headers}"
    );
    std::thread::sleep(Duration::from_millis(100));
    submit(&hub);
    let (id, event, data) = next_event(&mut reader);
    assert_eq!(event, None);
    assert_eq!(data["type"], "run.started", "{data}");
    assert_eq!(
        id.as_deref(),
        data["sequence"].as_u64().map(|n| n.to_string()).as_deref()
    );
}

#[test]
fn a_stream_is_refused_before_it_starts_for_an_unknown_session_or_a_bad_cursor() {
    let (_, addr) = started();
    let refused = |path: &str| {
        let mut reader = open_stream(addr, path);
        let (status, _) = status_and_headers(&mut reader);
        let mut body = Vec::new();
        let _ = reader.read_to_end(&mut body);
        let envelope: Value = serde_json::from_slice(&body).unwrap();
        (status, envelope["payload"]["error"]["code"].clone())
    };
    assert_eq!(
        refused("/sessions/ghost/events"),
        (404, "unknown_session".into())
    );
    assert_eq!(
        refused("/sessions/s/events?after=x"),
        (400, "invalid_cursor".into())
    );
    assert_eq!(
        refused("/sessions/s/events?run_id=r"),
        (400, "invalid_cursor".into())
    );
}

#[test]
fn a_client_that_hangs_up_on_an_idle_stream_frees_its_connection() {
    let hub = Hub::new(Registry::default_registry());
    block_on(hub.open(
        "memory",
        OpenRequest {
            session_id: "s".into(),
            ..OpenRequest::default()
        },
    ))
    .unwrap();
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let addr = listener.local_addr().unwrap();
    std::thread::spawn(move || {
        serve_http(
            &hub,
            listener,
            &HttpOptions {
                max_connections: 1,
                ..HttpOptions::default()
            },
        )
    });
    let mut reader = open_stream(addr, "/sessions/s/events");
    assert_eq!(status_and_headers(&mut reader).0, 200);
    drop(reader);
    std::thread::sleep(Duration::from_millis(300));
    let mut stream = TcpStream::connect(addr).unwrap();
    stream
        .set_read_timeout(Some(Duration::from_secs(5)))
        .unwrap();
    stream
        .write_all(format!("GET /adapters HTTP/1.1\r\nHost: {addr}\r\n\r\n").as_bytes())
        .unwrap();
    let mut raw = Vec::new();
    let _ = stream.read_to_end(&mut raw);
    assert!(
        raw.starts_with(b"HTTP/1.1 200"),
        "{}",
        String::from_utf8_lossy(&raw)
    );
}

#[test]
fn a_stream_the_server_ends_closes_the_connection() {
    let (hub, addr) = started();
    let mut reader = open_stream(addr, "/sessions/s/events");
    assert_eq!(status_and_headers(&mut reader).0, 200);
    std::thread::sleep(Duration::from_millis(100));
    block_on(hub.session(&"s".into()).unwrap().close()).unwrap();
    let mut rest = Vec::new();
    reader
        .read_to_end(&mut rest)
        .expect("the connection stayed open after the stream ended");
}
