use std::io::{Read, Write};
use std::net::{SocketAddr, TcpListener, TcpStream};

use oap_serve::http::{serve_http, HttpOptions};
use oap_serve::{Hub, Registry};
use serde_json::Value;

fn started() -> (Hub, SocketAddr) {
    let hub = Hub::new(Registry::default_registry());
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let addr = listener.local_addr().unwrap();
    let served = hub.clone();
    std::thread::spawn(move || serve_http(&served, listener, &HttpOptions::default()));
    (hub, addr)
}

fn send(addr: SocketAddr, raw: String) -> (u16, Value) {
    let mut stream = TcpStream::connect(addr).unwrap();
    stream
        .set_read_timeout(Some(std::time::Duration::from_secs(10)))
        .unwrap();
    stream.write_all(raw.as_bytes()).unwrap();
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

fn get(addr: SocketAddr, path: &str) -> (u16, Value) {
    send(addr, format!("GET {path} HTTP/1.1\r\nHost: {addr}\r\n\r\n"))
}

fn post(addr: SocketAddr, path: &str, body: &str) -> (u16, Value) {
    send(
        addr,
        format!(
            "POST {path} HTTP/1.1\r\nHost: {addr}\r\nContent-Type: application/json\r\nContent-Length: {}\r\n\r\n{body}",
            body.len()
        ),
    )
}

fn refusal(answer: &(u16, Value)) -> (u16, Value) {
    (answer.0, answer.1["payload"]["error"]["code"].clone())
}

#[test]
fn work_started_over_http_is_sent_to_read_listed_and_stopped() {
    let (hub, addr) = started();
    let (status, work) = post(
        addr,
        "/adapters/memory/work",
        r#"{"message":"go","title":"T"}"#,
    );
    assert_eq!(status, 200, "{work}");
    assert_eq!(work["title"], "T");
    let id = work["ref"]["session_id"].as_str().unwrap().to_owned();
    let (status, sent) = post(
        addr,
        &format!("/work/sessions/{id}/send"),
        r#"{"message":"more"}"#,
    );
    assert_eq!(status, 200, "{sent}");
    let texts: Vec<String> = hub
        .session(&id.as_str().into())
        .unwrap()
        .turns(None, 10)
        .turns
        .iter()
        .map(|turn| turn.text.clone())
        .collect();
    assert_eq!(texts, ["go", "more"]);
    let (status, status_answer) = get(addr, &format!("/work/sessions/{id}"));
    assert_eq!(status, 200, "{status_answer}");
    assert_eq!(status_answer["ref"]["session_id"], id.as_str());
    let (status, read) = get(addr, &format!("/work/sessions/{id}/read?limit=1"));
    assert_eq!(status, 200, "{read}");
    let (status, listed) = get(addr, "/work?adapters=memory");
    assert_eq!(status, 200, "{listed}");
    assert!(listed.to_string().contains(&id), "{listed}");
    let (status, capabilities) = get(addr, "/work/capabilities");
    assert_eq!(status, 200, "{capabilities}");
    let (status, stopped) = post(addr, &format!("/work/sessions/{id}/stop"), "");
    assert_eq!(status, 200, "{stopped}");
}

#[test]
fn the_work_routes_refuse_with_gos_statuses() {
    let (_, addr) = started();
    assert_eq!(
        refusal(&post(addr, "/adapters/nope/work", r#"{"message":"go"}"#)),
        (404, "unknown_adapter".into())
    );
    assert_eq!(
        refusal(&post(addr, "/adapters/memory/work", "[1]")),
        (400, "malformed_json".into())
    );
    assert_eq!(
        refusal(&post(addr, "/adapters/memory/work", "")),
        (400, "malformed_json".into())
    );
    assert_eq!(
        refusal(&get(addr, "/work/sessions/absent")),
        (404, "unknown_session".into())
    );
    assert_eq!(
        refusal(&get(addr, "/work/sessions/absent/read?after=%2B1")),
        (400, "invalid_request".into())
    );
    assert_eq!(
        refusal(&get(addr, "/work?limit=x")),
        (400, "invalid_request".into())
    );
    assert_eq!(
        refusal(&get(addr, "/work?limit=101")),
        (400, "invalid_request".into())
    );
}
