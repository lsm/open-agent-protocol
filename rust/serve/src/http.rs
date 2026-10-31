use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;

use serde_json::Value;

use crate::stdio::{self, Ids, WireError};
use crate::Hub;

pub const DEFAULT_ADDR: &str = "127.0.0.1:6270";
pub const DEFAULT_MAX_CONNECTIONS: usize = 64;
const MAX_HEADER_BYTES: usize = 64 << 10;

pub const DEFAULT_HEADER_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(30);

#[derive(Clone, Debug, Default)]
pub struct HttpOptions {
    pub host_allowlist: Vec<String>,
    pub max_connections: usize,
    pub header_timeout: Option<std::time::Duration>,
}

struct Served {
    hub: Hub,
    allow: Vec<String>,
    ids: Ids,
    open: AtomicUsize,
    max: usize,
    header_timeout: std::time::Duration,
}

pub fn serve_http(hub: &Hub, listener: TcpListener, options: &HttpOptions) -> std::io::Result<()> {
    let served = Arc::new(Served {
        hub: hub.clone(),
        allow: options
            .host_allowlist
            .iter()
            .map(|host| host.trim().to_ascii_lowercase())
            .collect(),
        ids: Ids::default(),
        open: AtomicUsize::new(0),
        max: match options.max_connections {
            0 => DEFAULT_MAX_CONNECTIONS,
            max => max,
        },
        header_timeout: options.header_timeout.unwrap_or(DEFAULT_HEADER_TIMEOUT),
    });
    for stream in listener.incoming() {
        let stream = stream?;
        if served.open.fetch_add(1, Ordering::SeqCst) >= served.max {
            served.open.fetch_sub(1, Ordering::SeqCst);
            continue;
        }
        let served = served.clone();
        std::thread::spawn(move || {
            let _ = served.connection(stream);
            served.open.fetch_sub(1, Ordering::SeqCst);
        });
    }
    Ok(())
}

pub(crate) struct Asked {
    pub(crate) method: String,
    pub(crate) path: String,
    pub(crate) query: Vec<(String, String)>,
    pub(crate) headers: Vec<(String, String)>,
}

impl Asked {
    pub(crate) fn header(&self, name: &str) -> Option<&str> {
        self.headers
            .iter()
            .find(|(key, _)| key.eq_ignore_ascii_case(name))
            .map(|(_, value)| value.as_str())
    }

    fn query(&self, name: &str) -> Option<&str> {
        self.query
            .iter()
            .find(|(key, _)| key == name)
            .map(|(_, value)| value.as_str())
    }
}

pub(crate) struct Answer {
    pub(crate) status: u16,
    pub(crate) content_type: &'static str,
    pub(crate) allow: Option<&'static str>,
    pub(crate) body: Vec<u8>,
}

fn decode(text: &str, plus_is_space: bool) -> String {
    let bytes = text.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut at = 0;
    while at < bytes.len() {
        let hex = || std::str::from_utf8(bytes.get(at + 1..at + 3)?).ok();
        match (
            bytes[at],
            hex().and_then(|hex| u8::from_str_radix(hex, 16).ok()),
        ) {
            (b'%', Some(byte)) => {
                out.push(byte);
                at += 3;
            }
            (b'+', _) if plus_is_space => {
                out.push(b' ');
                at += 1;
            }
            (byte, _) => {
                out.push(byte);
                at += 1;
            }
        }
    }
    String::from_utf8_lossy(&out).into_owned()
}

enum Head {
    Read(Vec<u8>),
    TooLarge,
    Gone,
}

fn read_head(stream: &TcpStream, budget: std::time::Duration) -> std::io::Result<Head> {
    let deadline = std::time::Instant::now() + budget;
    let mut head = Vec::new();
    let mut chunk = [0u8; 4096];
    loop {
        let left = deadline.saturating_duration_since(std::time::Instant::now());
        if left.is_zero() {
            return Ok(Head::Gone);
        }
        stream.set_read_timeout(Some(left))?;
        let read = match (&*stream).read(&mut chunk) {
            Ok(read) => read,
            Err(err)
                if matches!(
                    err.kind(),
                    std::io::ErrorKind::WouldBlock | std::io::ErrorKind::TimedOut
                ) =>
            {
                return Ok(Head::Gone)
            }
            Err(err) => return Err(err),
        };
        if read == 0 {
            return Ok(Head::Gone);
        }
        head.extend_from_slice(&chunk[..read]);
        if let Some(end) = head.windows(4).position(|window| window == b"\r\n\r\n") {
            head.truncate(end + 4);
            return Ok(Head::Read(head));
        }
        if head.len() > MAX_HEADER_BYTES {
            return Ok(Head::TooLarge);
        }
    }
}

impl Served {
    fn connection(&self, stream: TcpStream) -> std::io::Result<()> {
        let head = match read_head(&stream, self.header_timeout)? {
            Head::Read(head) => head,
            Head::TooLarge => {
                return respond(
                    &stream,
                    &plain(431, "431 Request Header Fields Too Large\n"),
                    true,
                )
            }
            Head::Gone => return Ok(()),
        };
        let mut headers = [httparse::EMPTY_HEADER; 64];
        let mut parsed = httparse::Request::new(&mut headers);
        if !matches!(parsed.parse(&head), Ok(httparse::Status::Complete(_))) {
            return respond(&stream, &plain(400, "400 Bad Request\n"), true);
        }
        let target = parsed.path.unwrap_or("/");
        let (path, query) = target.split_once('?').unwrap_or((target, ""));
        let asked = Asked {
            method: parsed.method.unwrap_or("GET").to_owned(),
            path: decode(path, false),
            query: query
                .split('&')
                .filter(|pair| !pair.is_empty())
                .map(|pair| {
                    let (key, value) = pair.split_once('=').unwrap_or((pair, ""));
                    (decode(key, true), decode(value, true))
                })
                .collect(),
            headers: parsed
                .headers
                .iter()
                .map(|header| {
                    (
                        header.name.to_owned(),
                        String::from_utf8_lossy(header.value).into_owned(),
                    )
                })
                .collect(),
        };
        let answer = self.answer(&asked);
        respond(&stream, &answer, asked.method != "HEAD")
    }

    fn answer(&self, asked: &Asked) -> Answer {
        if asked.header("Origin").is_some() {
            return self.refused(
                403,
                stdio::wire(
                    "cross_origin_request",
                    "the daemon does not serve cross-origin requests",
                ),
                "",
            );
        }
        if !self.allow.is_empty() {
            let host = asked
                .header("Host")
                .unwrap_or_default()
                .trim()
                .to_ascii_lowercase();
            let name = match host.rsplit_once(':') {
                Some((name, port)) if port.chars().all(|c| c.is_ascii_digit()) => name,
                _ => host.as_str(),
            };
            let name = name.trim_start_matches('[').trim_end_matches(']');
            if !self
                .allow
                .iter()
                .any(|allowed| allowed.trim_start_matches('[').trim_end_matches(']') == name)
            {
                return self.refused(
                    403,
                    stdio::wire(
                        "unrecognized_host",
                        "unrecognized Host header; this daemon serves loopback clients only",
                    ),
                    "",
                );
            }
        }
        let segments: Vec<&str> = asked.path.trim_start_matches('/').split('/').collect();
        let method = if asked.method == "HEAD" {
            "GET"
        } else {
            asked.method.as_str()
        };
        let (allowed, op, session, adapter) = match segments.as_slice() {
            ["adapters"] => ("GET", "adapters", "", ""),
            ["adapters", name, "capabilities"] => ("GET", "capabilities", "", *name),
            ["sessions"] => ("GET", "sessions", "", ""),
            ["sessions", "history"] => ("GET", "history", "", ""),
            ["sessions", id, "state"] => ("GET", "state", *id, ""),
            _ => return plain(404, "404 page not found\n"),
        };
        if method != allowed {
            return Answer {
                allow: Some(allowed),
                ..plain(405, "Method Not Allowed\n")
            };
        }
        let mut request = stdio::Request {
            op: op.to_owned(),
            session_id: session.to_owned(),
            adapter: adapter.to_owned(),
            cursor: asked.query("cursor").unwrap_or_default().to_owned(),
            ..stdio::Request::default()
        };
        if let Some(limit) = asked.query("limit").filter(|_| op == "history") {
            match limit.parse::<i64>() {
                Ok(limit) => request.limit = Some(limit),
                Err(_) => {
                    return self.refused(
                        400,
                        stdio::wire("invalid_request", oap_binding::Error::InvalidLimit),
                        session,
                    )
                }
            }
        }
        match stdio::dispatch(&self.hub, &request, &self.ids, usize::MAX) {
            Ok(value) => json(200, &value),
            Err(error) => {
                let status = status_of(error.code);
                self.refused(status, error, session)
            }
        }
    }

    fn refused(&self, status: u16, error: WireError, session: &str) -> Answer {
        let next = |kind: &str| {
            let next = self.ids.fetch_add(1, Ordering::SeqCst) + 1;
            format!("oap-{kind}-{next}")
        };
        let mut payload =
            serde_json::json!({ "error": { "code": error.code, "message": error.message } });
        if let Some(details) = error.details {
            payload["error"]["details"] = details;
        }
        let mut envelope = match oap_protocol::Envelope::new(
            oap_protocol::EnvelopeType::ERROR_RESPONSE,
            next("error"),
            &payload,
        ) {
            Ok(envelope) => envelope,
            Err(_) => return plain(500, "oap: internal error\n"),
        };
        envelope.in_reply_to = Some(next("request").into());
        if !session.is_empty() {
            envelope.session_id = Some(session.into());
        }
        match serde_json::to_value(&envelope) {
            Ok(value) => json(status, &value),
            Err(_) => plain(500, "oap: internal error\n"),
        }
    }
}

pub(crate) fn status_of(code: &str) -> u16 {
    match code {
        "unknown_session" | "unknown_adapter" | "run_not_found" => 404,
        "session_closed" | "run_active" | "session_exists" | "run_terminal" => 409,
        "internal" | "probe_failed" | "state_failed" | "history_failed" | "open_failed" => 500,
        _ => 400,
    }
}

fn plain(status: u16, body: &str) -> Answer {
    Answer {
        status,
        content_type: "text/plain; charset=utf-8",
        allow: None,
        body: body.as_bytes().to_vec(),
    }
}

fn json(status: u16, value: &Value) -> Answer {
    Answer {
        status,
        content_type: "application/json",
        allow: None,
        body: serde_json::to_vec(value).unwrap_or_default(),
    }
}

fn reason(status: u16) -> &'static str {
    match status {
        200 => "OK",
        204 => "No Content",
        400 => "Bad Request",
        403 => "Forbidden",
        404 => "Not Found",
        405 => "Method Not Allowed",
        409 => "Conflict",
        413 => "Payload Too Large",
        431 => "Request Header Fields Too Large",
        415 => "Unsupported Media Type",
        503 => "Service Unavailable",
        _ => "Internal Server Error",
    }
}

fn respond(mut stream: &TcpStream, answer: &Answer, with_body: bool) -> std::io::Result<()> {
    let mut head = format!(
        "HTTP/1.1 {} {}\r\nContent-Type: {}\r\nContent-Length: {}\r\nConnection: close\r\n",
        answer.status,
        reason(answer.status),
        answer.content_type,
        answer.body.len()
    );
    if let Some(allow) = answer.allow {
        head.push_str(&format!("Allow: {allow}\r\n"));
    }
    head.push_str("\r\n");
    stream.write_all(head.as_bytes())?;
    if with_body {
        stream.write_all(&answer.body)?;
    }
    stream.flush()
}
