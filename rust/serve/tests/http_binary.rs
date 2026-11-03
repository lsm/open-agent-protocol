use std::io::{BufRead, BufReader, Read, Write};
use std::net::TcpStream;
use std::process::{Child, Command, Stdio};
use std::time::{Duration, Instant};

fn started() -> (Child, String) {
    let mut child = Command::new(env!("CARGO_BIN_EXE_oap-rust-hub"))
        .args(["serve", "--addr", "127.0.0.1:0"])
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .unwrap();
    let mut line = String::new();
    BufReader::new(child.stdout.take().unwrap())
        .read_line(&mut line)
        .unwrap();
    let addr = line
        .trim()
        .strip_prefix("listening on http://")
        .unwrap_or_else(|| panic!("{line:?}"))
        .to_owned();
    (child, addr)
}

fn ask(addr: &str, head: &str, body: &str) -> (u16, String) {
    let mut stream = TcpStream::connect(addr).unwrap();
    stream
        .set_read_timeout(Some(Duration::from_secs(10)))
        .unwrap();
    stream
        .write_all(format!("{head}\r\nContent-Length: {}\r\n\r\n{body}", body.len()).as_bytes())
        .unwrap();
    let mut out = String::new();
    stream.read_to_string(&mut out).unwrap();
    (out[9..12].parse().unwrap(), out)
}

#[test]
fn serve_with_an_address_answers_loopback_hosts_over_http_until_it_is_shut_down() {
    let (mut child, addr) = started();
    let (status, answer) = ask(
        &addr,
        &format!("GET /adapters HTTP/1.1\r\nHost: {addr}"),
        "",
    );
    assert_eq!(status, 200, "{answer}");
    assert!(answer.contains("memory"), "{answer}");
    let (status, _) = ask(&addr, "GET /adapters HTTP/1.1\r\nHost: example.com", "");
    assert_eq!(status, 403);
    let (status, answer) = ask(
        &addr,
        &format!("POST /shutdown HTTP/1.1\r\nHost: {addr}\r\nContent-Type: application/json"),
        "{}",
    );
    assert_eq!(status, 200, "{answer}");
    assert!(answer.contains("\"stopping\""), "{answer}");
    let deadline = Instant::now() + Duration::from_secs(10);
    let exited = loop {
        if let Some(status) = child.try_wait().unwrap() {
            break status;
        }
        assert!(Instant::now() < deadline, "the hub went on serving");
        std::thread::sleep(Duration::from_millis(20));
    };
    assert!(exited.success());
}

#[test]
fn an_address_and_stdio_together_are_refused() {
    let status = Command::new(env!("CARGO_BIN_EXE_oap-rust-hub"))
        .args(["serve", "--stdio", "--addr", "127.0.0.1:0"])
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .unwrap();
    assert_eq!(status.code(), Some(2));
}
