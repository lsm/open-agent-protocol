use std::io::{Read, Write};
use std::net::{SocketAddr, TcpListener, TcpStream};

use futures::executor::block_on;
use oap_serve::http::{serve_http, HttpOptions};
use oap_serve::{Hub, Registry};
use serde_json::Value;

fn started(hub: Hub, options: HttpOptions) -> SocketAddr {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let addr = listener.local_addr().unwrap();
    std::thread::spawn(move || serve_http(&hub, listener, &options));
    addr
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

fn ask(addr: SocketAddr, head: &str) -> Reply {
    let mut stream = TcpStream::connect(addr).unwrap();
    stream.write_all(head.as_bytes()).unwrap();
    let mut raw = Vec::new();
    stream.read_to_end(&mut raw).unwrap();
    let split = raw
        .windows(4)
        .position(|window| window == b"\r\n\r\n")
        .unwrap();
    let headers = String::from_utf8(raw[..split].to_vec()).unwrap();
    let status = headers[9..12].parse().unwrap();
    Reply {
        status,
        headers,
        body: raw[split + 4..].to_vec(),
    }
}

fn get(addr: SocketAddr, path: &str) -> Reply {
    ask(
        addr,
        &format!("GET {path} HTTP/1.1\r\nHost: {addr}\r\n\r\n"),
    )
}

fn hub() -> Hub {
    Hub::new(Registry::default_registry())
}

#[test]
fn the_read_only_routes_answer_as_the_stdio_ops_do() {
    let hub = hub();
    block_on(hub.open(
        "memory",
        oap_adapter::OpenRequest {
            session_id: "s".into(),
            ..oap_adapter::OpenRequest::default()
        },
    ))
    .unwrap();
    let addr = started(hub, HttpOptions::default());
    let adapters = get(addr, "/adapters");
    assert_eq!(adapters.status, 200);
    assert!(adapters.headers.contains("Content-Type: application/json"));
    assert_eq!(adapters.json()["adapters"][0]["name"], "memory");
    assert_eq!(
        get(addr, "/sessions").json()["sessions"][0]["session_id"],
        "s"
    );
    let capabilities = get(addr, "/adapters/memory/capabilities").json();
    assert_eq!(capabilities["type"], "capabilities.response");
    let state = get(addr, "/sessions/s/state").json();
    assert_eq!(state["type"], "session.state.response");
    assert_eq!(state["payload"]["session_id"], "s");
}

#[test]
fn a_refusal_is_an_error_response_envelope_with_the_status_the_route_table_names() {
    let addr = started(hub(), HttpOptions::default());
    let unknown = get(addr, "/sessions/ghost/state");
    assert_eq!(unknown.status, 404);
    let envelope = unknown.json();
    assert_eq!(envelope["type"], "error.response");
    assert_eq!(envelope["payload"]["error"]["code"], "unknown_session");
    assert_eq!(envelope["session_id"], "ghost");
    assert!(envelope["in_reply_to"].is_string());
    assert_eq!(get(addr, "/adapters/ghost/capabilities").status, 404);
    let history = get(addr, "/sessions/history");
    assert_eq!(history.status, 400);
    assert_eq!(
        history.json()["payload"]["error"]["code"],
        "unsupported_feature"
    );
    let limit = get(addr, "/sessions/history?limit=ten");
    assert_eq!(limit.json()["payload"]["error"]["code"], "invalid_request");
}

#[test]
fn an_unknown_path_is_a_plain_404_a_wrong_method_is_405_naming_the_method_and_head_is_get() {
    let addr = started(hub(), HttpOptions::default());
    let missing = get(addr, "/nowhere");
    assert_eq!(missing.status, 404);
    assert_eq!(missing.body, b"404 page not found\n");
    let wrong = ask(
        addr,
        &format!("POST /adapters HTTP/1.1\r\nHost: {addr}\r\nContent-Length: 0\r\n\r\n"),
    );
    assert_eq!(wrong.status, 405);
    assert!(wrong.headers.contains("Allow: GET"), "{}", wrong.headers);
    let head = ask(
        addr,
        &format!("HEAD /sessions HTTP/1.1\r\nHost: {addr}\r\n\r\n"),
    );
    assert_eq!(head.status, 200);
    assert!(head.body.is_empty());
}

#[test]
fn a_browser_origin_and_an_unlisted_host_are_refused_403() {
    let addr = started(
        hub(),
        HttpOptions {
            host_allowlist: vec!["127.0.0.1".into(), "localhost".into()],
            ..HttpOptions::default()
        },
    );
    let origin = ask(
        addr,
        &format!("GET /adapters HTTP/1.1\r\nHost: {addr}\r\nOrigin: http://evil.example\r\n\r\n"),
    );
    assert_eq!(origin.status, 403);
    assert_eq!(
        origin.json()["payload"]["error"]["code"],
        "cross_origin_request"
    );
    let host = ask(addr, "GET /adapters HTTP/1.1\r\nHost: evil.example\r\n\r\n");
    assert_eq!(host.status, 403);
    assert_eq!(host.json()["payload"]["error"]["code"], "unrecognized_host");
    let local = ask(
        addr,
        &format!(
            "GET /adapters HTTP/1.1\r\nHost: localhost:{}\r\n\r\n",
            addr.port()
        ),
    );
    assert_eq!(local.status, 200);
}

#[test]
fn a_connection_past_the_bound_is_closed_unanswered_and_serving_resumes_after() {
    let addr = started(
        hub(),
        HttpOptions {
            max_connections: 1,
            ..HttpOptions::default()
        },
    );
    let held = TcpStream::connect(addr).unwrap();
    std::thread::sleep(std::time::Duration::from_millis(100));
    let mut refused = TcpStream::connect(addr).unwrap();
    let _ = refused.write_all(format!("GET /adapters HTTP/1.1\r\nHost: {addr}\r\n\r\n").as_bytes());
    let mut raw = Vec::new();
    let _ = refused.read_to_end(&mut raw);
    assert!(raw.is_empty());
    drop(held);
    std::thread::sleep(std::time::Duration::from_millis(100));
    assert_eq!(get(addr, "/adapters").status, 200);
}

fn read_within(stream: &mut TcpStream, wait: std::time::Duration) -> Vec<u8> {
    stream.set_read_timeout(Some(wait)).unwrap();
    let mut raw = Vec::new();
    let _ = stream.read_to_end(&mut raw);
    raw
}

#[test]
fn a_peer_that_connects_and_says_nothing_is_given_up_on() {
    let addr = started(
        hub(),
        HttpOptions {
            max_connections: 1,
            header_timeout: Some(std::time::Duration::from_millis(200)),
            ..HttpOptions::default()
        },
    );
    let mut silent = TcpStream::connect(addr).unwrap();
    assert!(read_within(&mut silent, std::time::Duration::from_secs(5)).is_empty());
    std::thread::sleep(std::time::Duration::from_millis(100));
    assert_eq!(get(addr, "/adapters").status, 200);
}

#[test]
fn a_gate_answers_on_the_head_without_waiting_for_a_declared_body() {
    let addr = started(hub(), HttpOptions::default());
    let mut stream = TcpStream::connect(addr).unwrap();
    stream
        .write_all(
            format!(
                "POST /sessions HTTP/1.1\r\nHost: {addr}\r\nOrigin: http://evil.test\r\nContent-Length: 4096\r\n\r\nhello"
            )
            .as_bytes(),
        )
        .unwrap();
    let raw = read_within(&mut stream, std::time::Duration::from_secs(5));
    assert!(
        raw.starts_with(b"HTTP/1.1 403"),
        "{}",
        String::from_utf8_lossy(&raw)
    );
}

#[test]
fn a_head_past_its_budget_is_431_and_a_malformed_one_is_a_400_that_carries_its_body() {
    let addr = started(hub(), HttpOptions::default());
    let mut endless = TcpStream::connect(addr).unwrap();
    let _ = endless.write_all(&vec![b'a'; 70 * 1024]);
    let raw = read_within(&mut endless, std::time::Duration::from_secs(5));
    assert!(
        raw.starts_with(b"HTTP/1.1 431"),
        "{}",
        String::from_utf8_lossy(&raw)
    );
    let garbage = ask(addr, "GARBAGE\r\n\r\n");
    assert_eq!(garbage.status, 400);
    assert_eq!(garbage.body, b"400 Bad Request\n");
}

#[test]
fn a_query_limit_is_read_only_by_the_route_that_takes_one_and_a_path_keeps_its_plus() {
    let hub = hub();
    block_on(hub.open(
        "memory",
        oap_adapter::OpenRequest {
            session_id: "a+b".into(),
            ..oap_adapter::OpenRequest::default()
        },
    ))
    .unwrap();
    let addr = started(hub, HttpOptions::default());
    assert_eq!(get(addr, "/adapters?limit=ten").status, 200);
    let state = get(addr, "/sessions/a+b/state");
    assert_eq!(
        state.status,
        200,
        "{}",
        String::from_utf8_lossy(&state.body)
    );
    assert_eq!(state.json()["session_id"], "a+b");
}

#[test]
fn a_path_with_an_empty_segment_matches_no_route() {
    let addr = started(hub(), HttpOptions::default());
    for path in [
        "/adapters//capabilities",
        "/sessions//state",
        "//adapters",
        "/adapters/",
    ] {
        let reply = get(addr, path);
        assert_eq!(reply.status, 404, "{path}");
        assert_eq!(reply.body, b"404 page not found\n", "{path}");
    }
}
