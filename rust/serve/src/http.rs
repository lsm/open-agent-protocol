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
const DRAIN_BYTES: u64 = 256 << 10;
const MAX_BODY_BYTES: u64 = 16 << 20;
const CHUNK_EXCESS: u64 = 16 << 10;
const DRAIN_WAIT: std::time::Duration = std::time::Duration::from_secs(1);

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
        let stream = match stream {
            Ok(stream) => stream,
            Err(err) => match accepted(&err) {
                Accept::Again => continue,
                Accept::Later => {
                    std::thread::sleep(std::time::Duration::from_millis(10));
                    continue;
                }
                Accept::Stop => return Err(err),
            },
        };
        if served.open.fetch_add(1, Ordering::SeqCst) >= served.max {
            served.open.fetch_sub(1, Ordering::SeqCst);
            continue;
        }
        let served = served.clone();
        std::thread::spawn(move || {
            let _slot = Slot(&served.open);
            let _ = served.connection(stream);
        });
    }
    Ok(())
}

#[derive(Debug, PartialEq, Eq)]
enum Accept {
    Again,
    Later,
    Stop,
}

fn accepted(err: &std::io::Error) -> Accept {
    use std::io::ErrorKind;
    match err.kind() {
        ErrorKind::ConnectionAborted
        | ErrorKind::ConnectionReset
        | ErrorKind::ConnectionRefused
        | ErrorKind::Interrupted
        | ErrorKind::WouldBlock
        | ErrorKind::TimedOut => Accept::Again,
        _ if matches!(err.raw_os_error(), Some(23 | 24)) => Accept::Later,
        _ => Accept::Stop,
    }
}

struct Slot<'a>(&'a AtomicUsize);

impl Drop for Slot<'_> {
    fn drop(&mut self) {
        self.0.fetch_sub(1, Ordering::SeqCst);
    }
}

pub(crate) struct Asked {
    pub(crate) method: String,
    pub(crate) path: String,
    pub(crate) query: Vec<(String, String)>,
    pub(crate) headers: Vec<(String, String)>,
    pub(crate) rest: Vec<u8>,
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
    pub(crate) events: Option<Events>,
}

pub(crate) enum Events {
    Live(crate::Subscription),
    Gap(oap_adapter::ReplayGap),
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
    Read(Vec<u8>, Vec<u8>),
    TooLarge(Vec<u8>),
    Gone,
}

fn drain(stream: &TcpStream, cap: u64) {
    let deadline = std::time::Instant::now() + DRAIN_WAIT;
    let mut left = cap;
    let mut chunk = [0u8; 4096];
    while left > 0 {
        let wait = deadline.saturating_duration_since(std::time::Instant::now());
        if wait.is_zero() || stream.set_read_timeout(Some(wait)).is_err() {
            return;
        }
        let want = chunk.len().min(left as usize);
        match (&*stream).read(&mut chunk[..want]) {
            Ok(0) | Err(_) => return,
            Ok(read) => left -= read as u64,
        }
    }
}

fn too_large() -> (u16, WireError) {
    (
        413,
        stdio::wire("request_too_large", "request body exceeds the daemon limit"),
    )
}

fn json_media(header: &str) -> bool {
    let (media, mut rest) = header.split_at(header.find(';').unwrap_or(header.len()));
    if !media.trim().eq_ignore_ascii_case("application/json") {
        return false;
    }
    let mut seen = std::collections::HashMap::new();
    loop {
        rest = rest.trim_start();
        if rest.is_empty() {
            return true;
        }
        match media_parameter(rest) {
            Some((key, value, after)) => {
                let earlier = seen.insert(key.to_ascii_lowercase(), value.clone());
                if earlier.is_some_and(|earlier| earlier != value) {
                    return false;
                }
                rest = after;
            }
            None => return rest.trim() == ";",
        }
    }
}

fn token_end(text: &str) -> usize {
    text.find(|c: char| !c.is_ascii_graphic() || "()<>@,;:\\\"/[]?=".contains(c))
        .unwrap_or(text.len())
}

fn media_parameter(text: &str) -> Option<(&str, String, &str)> {
    let text = text.trim_start().strip_prefix(';')?.trim_start();
    let end = token_end(text);
    let (key, text) = text.split_at(end);
    if key.is_empty() {
        return None;
    }
    let text = text.trim_start().strip_prefix('=')?.trim_start();
    if let Some(quoted) = text.strip_prefix('"') {
        let mut value = String::new();
        let mut escaped = false;
        for (at, c) in quoted.char_indices() {
            match c {
                _ if escaped => {
                    escaped = false;
                    value.push(c);
                }
                '\\' => escaped = true,
                '"' => return Some((key, value, &quoted[at + 1..])),
                _ => value.push(c),
            }
        }
        return None;
    }
    let end = token_end(text);
    if end == 0 {
        return None;
    }
    Some((key, text[..end].to_owned(), &text[end..]))
}

enum Refused {
    TooLarge,
    Unreadable,
}

struct Reading<'a> {
    stream: &'a TcpStream,
    held: Vec<u8>,
    at: usize,
    deadline: std::time::Instant,
}

impl Reading<'_> {
    fn fill(&mut self) -> bool {
        if self.at < self.held.len() {
            return true;
        }
        let wait = self
            .deadline
            .saturating_duration_since(std::time::Instant::now());
        if wait.is_zero() || self.stream.set_read_timeout(Some(wait)).is_err() {
            return false;
        }
        let mut chunk = [0u8; 8192];
        match self.stream.read(&mut chunk) {
            Ok(0) | Err(_) => false,
            Ok(read) => {
                self.held = chunk[..read].to_vec();
                self.at = 0;
                true
            }
        }
    }

    fn exact(&mut self, length: usize) -> Option<Vec<u8>> {
        let mut out = Vec::with_capacity(length.min(1 << 20));
        while out.len() < length {
            if !self.fill() {
                return None;
            }
            let take = (length - out.len()).min(self.held.len() - self.at);
            out.extend_from_slice(&self.held[self.at..self.at + take]);
            self.at += take;
        }
        Some(out)
    }

    fn line(&mut self) -> Option<Vec<u8>> {
        let mut line = Vec::new();
        loop {
            if !self.fill() || line.len() > 4096 {
                return None;
            }
            let byte = self.held[self.at];
            self.at += 1;
            if byte == b'\n' {
                if line.last() == Some(&b'\r') {
                    line.pop();
                }
                return Some(line);
            }
            line.push(byte);
        }
    }

    fn chunked(&mut self) -> Result<Vec<u8>, Refused> {
        let mut body = Vec::new();
        let mut excess: u64 = 0;
        loop {
            let line = self.line().ok_or(Refused::Unreadable)?;
            let text = String::from_utf8_lossy(&line);
            let size = text.split(';').next().unwrap_or_default().trim();
            let size = u64::from_str_radix(size, 16).map_err(|_| Refused::Unreadable)?;
            excess = (excess + line.len() as u64 + 2)
                .saturating_sub(16u64.saturating_add(size.saturating_mul(2)));
            if excess > CHUNK_EXCESS {
                return Err(Refused::Unreadable);
            }
            if size == 0 {
                while !self.line().ok_or(Refused::Unreadable)?.is_empty() {}
                return Ok(body);
            }
            if (body.len() as u64)
                .checked_add(size)
                .is_none_or(|total| total > MAX_BODY_BYTES)
            {
                return Err(Refused::TooLarge);
            }
            body.extend(self.exact(size as usize).ok_or(Refused::Unreadable)?);
            if !self.line().ok_or(Refused::Unreadable)?.is_empty() {
                return Err(Refused::Unreadable);
            }
        }
    }
}

fn declared(asked: &Asked) -> u64 {
    asked
        .header("Content-Length")
        .and_then(|length| length.trim().parse().ok())
        .unwrap_or(0)
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
            let rest = head.split_off(end + 4);
            return Ok(Head::Read(head, rest));
        }
        if head.len() > MAX_HEADER_BYTES {
            return Ok(Head::TooLarge(head));
        }
    }
}

impl Served {
    fn connection(&self, stream: TcpStream) -> std::io::Result<()> {
        let (head, rest) = match read_head(&stream, self.header_timeout)? {
            Head::Read(head, rest) => (head, rest),
            Head::TooLarge(head) => {
                return respond(
                    &stream,
                    &plain(431, "431 Request Header Fields Too Large\n"),
                    !head.starts_with(b"HEAD "),
                )
            }
            Head::Gone => return Ok(()),
        };
        let mut headers = [httparse::EMPTY_HEADER; 64];
        let mut parsed = httparse::Request::new(&mut headers);
        if !matches!(parsed.parse(&head), Ok(httparse::Status::Complete(_))) {
            return respond(
                &stream,
                &plain(400, "400 Bad Request\n"),
                !head.starts_with(b"HEAD "),
            );
        }
        let target = parsed.path.unwrap_or("/");
        let (path, query) = target.split_once('?').unwrap_or((target, ""));
        let asked = Asked {
            method: parsed.method.unwrap_or("GET").to_owned(),
            path: path.to_owned(),
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
            rest,
        };
        let mut consumed = false;
        let codings = asked
            .headers
            .iter()
            .filter(|(key, _)| key.eq_ignore_ascii_case("Transfer-Encoding"))
            .count();
        let lengths: Vec<&str> = asked
            .headers
            .iter()
            .filter(|(key, _)| key.eq_ignore_ascii_case("Content-Length"))
            .map(|(_, value)| value.trim())
            .collect();
        let unreadable_length = lengths.iter().any(|length| {
            *length != lengths[0]
                || length.is_empty()
                || !length.bytes().all(|b| b.is_ascii_digit())
        });
        let other_coding = asked
            .header("Transfer-Encoding")
            .is_some_and(|coding| !coding.trim().eq_ignore_ascii_case("chunked"));
        if codings > 1 || (codings == 1 && !lengths.is_empty()) || unreadable_length || other_coding
        {
            respond(
                &stream,
                &plain(400, "400 Bad Request\n"),
                asked.method != "HEAD",
            )?;
            drain(&stream, DRAIN_BYTES);
            return Ok(());
        }
        let mut answer = self.answer(&asked, &stream, &mut consumed);
        stream.set_write_timeout(Some(self.header_timeout))?;
        if let Some(events) = answer.events.take() {
            return stream_events(&stream, events, asked.method != "HEAD");
        }
        respond(&stream, &answer, asked.method != "HEAD")?;
        if !consumed {
            let left = if asked.header("Transfer-Encoding").is_some() {
                DRAIN_BYTES
            } else {
                declared(&asked).saturating_sub(asked.rest.len() as u64)
            };
            drain(&stream, left.min(DRAIN_BYTES));
        }
        Ok(())
    }

    fn answer(&self, asked: &Asked, stream: &TcpStream, consumed: &mut bool) -> Answer {
        if asked.header("Origin").is_some() {
            return self.refused(
                403,
                stdio::wire(
                    "cross_origin_request",
                    "the daemon does not serve cross-origin requests",
                ),
                "",
                None,
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
                    None,
                );
            }
        }
        let raw: Vec<&str> = asked
            .path
            .strip_prefix('/')
            .unwrap_or_default()
            .split('/')
            .collect();
        if !asked.path.starts_with('/') || raw.iter().any(|segment| segment.is_empty()) {
            return plain(404, "404 page not found\n");
        }
        let decoded: Vec<String> = raw.iter().map(|segment| decode(segment, false)).collect();
        let segments: Vec<&str> = decoded.iter().map(String::as_str).collect();
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
            ["sessions", id, "tools"] => ("GET", "tools", *id, ""),
            ["sessions", id, "models"] => ("GET", "models", *id, ""),
            ["sessions", id, "events"] => ("GET", "events", *id, ""),
            ["sessions", id, "submit"] => ("POST", "submit", *id, ""),
            ["sessions", id, "resolve"] => ("POST", "resolve", *id, ""),
            ["sessions", id, "cancel"] => ("POST", "cancel", *id, ""),
            ["sessions", id, "settings"] => ("POST", "settings", *id, ""),
            ["sessions", id, "close"] => ("POST", "close", *id, ""),
            _ => return plain(404, "404 page not found\n"),
        };
        if method != allowed {
            return Answer {
                allow: Some(allowed),
                ..plain(405, "Method Not Allowed\n")
            };
        }
        if op == "events" {
            return self.events(asked, session);
        }
        let mut request = stdio::Request {
            op: op.to_owned(),
            session_id: session.to_owned(),
            adapter: adapter.to_owned(),
            cursor: asked.query("cursor").unwrap_or_default().to_owned(),
            ..stdio::Request::default()
        };
        if matches!(op, "tools" | "models") {
            request.allow_degraded_features = asked
                .query
                .iter()
                .filter(|(key, _)| key == "allow_degraded")
                .map(|(_, value)| value.clone())
                .collect();
        }
        let mut body = Vec::new();
        if matches!(op, "submit" | "resolve" | "cancel" | "settings") {
            body = match self.body(asked, stream, consumed) {
                Ok(body) => body,
                Err((status, error)) => return self.refused(status, error, session, None),
            };
            match serde_json::value::RawValue::from_string(
                String::from_utf8_lossy(&body).into_owned(),
            ) {
                Ok(raw) => request.request = Some(raw),
                Err(err) => {
                    return self.refused(400, stdio::wire("malformed_json", err), session, None)
                }
            }
        }
        if let Some(limit) = asked.query("limit").filter(|_| op == "history") {
            match limit.parse::<i64>() {
                Ok(limit) => request.limit = Some(limit),
                Err(_) => {
                    return self.refused(
                        400,
                        stdio::wire("invalid_request", oap_binding::Error::InvalidLimit),
                        session,
                        None,
                    )
                }
            }
        }
        match stdio::dispatch(&self.hub, &request, &self.ids, usize::MAX) {
            Ok(Value::Null) if op == "close" => Answer {
                status: 204,
                content_type: "",
                allow: None,
                body: Vec::new(),
                events: None,
            },
            Ok(value) => json(200, &value),
            Err(error) => {
                let status = status_of(error.code);
                let correlated = (!body.is_empty()).then_some(body.as_slice());
                self.refused(status, error, session, correlated)
            }
        }
    }

    fn events(&self, asked: &Asked, session: &str) -> Answer {
        let refuse = |code: &'static str, message: String| {
            self.refused(status_of(code), stdio::wire(code, message), session, None)
        };
        let Ok(entry) = self.hub.session(&session.into()) else {
            return refuse("unknown_session", format!("no session {:?}", session));
        };
        if entry.is_closed() {
            return refuse("session_closed", "the session is closed".into());
        }
        let cursor = asked
            .query("after")
            .filter(|after| !after.is_empty())
            .or_else(|| asked.header("Last-Event-ID"))
            .unwrap_or_default()
            .trim()
            .to_owned();
        let run = asked.query("run_id").unwrap_or_default().to_owned();
        let subscribed = if cursor.is_empty() {
            if !run.is_empty() {
                return refuse(
                    "invalid_cursor",
                    "run_id names the run a cursor belongs to; it has no meaning without after"
                        .into(),
                );
            }
            self.hub.subscribe(entry.id())
        } else {
            let Some(after) = cursor
                .parse::<u64>()
                .ok()
                .filter(|_| cursor.bytes().all(|byte| byte.is_ascii_digit()))
            else {
                return refuse(
                    "invalid_cursor",
                    format!("cursor {cursor:?} is not an unsigned sequence"),
                );
            };
            let run = (!run.is_empty()).then(|| run.as_str().into());
            crate::live::block_on(self.hub.subscribe_after(entry.id(), run, after))
        };
        let events = match subscribed {
            Ok(subscription) => Events::Live(subscription),
            Err(crate::HubError::Adapter(oap_adapter::Error::ReplayGap(gap))) => Events::Gap(gap),
            Err(err) => {
                let code = match &err {
                    crate::HubError::Adapter(oap_adapter::Error::SessionClosed)
                    | crate::HubError::SessionClosed(_) => "session_closed",
                    crate::HubError::NoRunToResume => "no_run_to_resume",
                    crate::HubError::Adapter(oap_adapter::Error::ReplayCursorFuture) => {
                        "replay_cursor_future"
                    }
                    crate::HubError::Adapter(oap_adapter::Error::RunNotFound) => "run_not_found",
                    crate::HubError::UnknownSession(_) => "unknown_session",
                    _ => "internal",
                };
                return refuse(code, err.to_string());
            }
        };
        Answer {
            status: 200,
            content_type: "text/event-stream",
            allow: None,
            body: Vec::new(),
            events: Some(events),
        }
    }

    fn body(
        &self,
        asked: &Asked,
        stream: &TcpStream,
        consumed: &mut bool,
    ) -> Result<Vec<u8>, (u16, WireError)> {
        if !json_media(asked.header("Content-Type").unwrap_or_default()) {
            return Err((
                415,
                stdio::wire(
                    "unsupported_media_type",
                    "a request with a body declares application/json; the daemon reads no other media type",
                ),
            ));
        }
        let unreadable = || {
            (
                400,
                stdio::wire("request_read", "request body could not be read"),
            )
        };
        let deadline = std::time::Instant::now() + self.header_timeout;
        let mut reading = Reading {
            stream,
            held: asked.rest.clone(),
            at: 0,
            deadline,
        };
        if let Some(coding) = asked.header("Transfer-Encoding") {
            if !coding.trim().eq_ignore_ascii_case("chunked") {
                return Err(unreadable());
            }
            let body = reading.chunked().map_err(|refused| match refused {
                Refused::TooLarge => too_large(),
                Refused::Unreadable => unreadable(),
            })?;
            *consumed = true;
            return Ok(body);
        }
        let length = declared(asked);
        if length > MAX_BODY_BYTES {
            return Err(too_large());
        }
        let body = reading.exact(length as usize).ok_or_else(unreadable)?;
        *consumed = true;
        Ok(body)
    }

    fn refused(
        &self,
        status: u16,
        error: WireError,
        session: &str,
        request: Option<&[u8]>,
    ) -> Answer {
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
        let named = request
            .and_then(|request| serde_json::from_slice::<Value>(request).ok())
            .unwrap_or(Value::Null);
        let text = |key: &str| named[key].as_str().filter(|value| !value.is_empty());
        envelope.in_reply_to = Some(
            text("id")
                .map_or_else(|| next("request"), str::to_owned)
                .into(),
        );
        envelope.session_id = text("session_id")
            .or((!session.is_empty()).then_some(session))
            .map(Into::into);
        envelope.run_id = text("run_id")
            .or_else(|| {
                named["payload"]["run_id"]
                    .as_str()
                    .filter(|run| !run.is_empty())
            })
            .map(Into::into);
        match serde_json::to_value(&envelope) {
            Ok(value) => json(status, &value),
            Err(_) => plain(500, "oap: internal error\n"),
        }
    }
}

pub(crate) fn status_of(code: &str) -> u16 {
    match code {
        "unknown_session" | "unknown_adapter" | "run_not_found" => 404,
        "session_closed"
        | "run_active"
        | "session_exists"
        | "run_terminal"
        | "resolution_rejected"
        | "no_run_to_resume" => 409,
        "open_failed" | "tools_failed" => 502,
        "internal" | "probe_failed" | "state_failed" | "history_failed" => 500,
        _ => 400,
    }
}

fn plain(status: u16, body: &str) -> Answer {
    Answer {
        status,
        content_type: "text/plain; charset=utf-8",
        allow: None,
        body: body.as_bytes().to_vec(),
        events: None,
    }
}

fn json(status: u16, value: &Value) -> Answer {
    Answer {
        status,
        content_type: "application/json",
        allow: None,
        body: serde_json::to_vec(value).unwrap_or_default(),
        events: None,
    }
}

fn reason(status: u16) -> &'static str {
    match status {
        200 => "OK",
        502 => "Bad Gateway",
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

fn signal(mut stream: &TcpStream, event: &str, data: &Value) -> std::io::Result<()> {
    let line = format!("event: {event}\ndata: {}\n\n", serde_json::to_string(data)?);
    stream.write_all(line.as_bytes())?;
    stream.flush()
}

fn stream_events(mut stream: &TcpStream, events: Events, with_body: bool) -> std::io::Result<()> {
    stream.write_all(
        b"HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nCache-Control: no-cache\r\nConnection: close\r\n\r\n",
    )?;
    stream.flush()?;
    if !with_body {
        return Ok(());
    }
    let mut subscription = match events {
        Events::Gap(gap) => {
            return signal(
                stream,
                "oap-replay-gap",
                &serde_json::json!({
                    "requested_after": gap.requested_after,
                    "oldest_available": gap.oldest_available,
                    "latest_available": gap.latest_available,
                    "message": "requested replay cursor is no longer retained; reconnect with a cursor at or after oldest_available - 1",
                }),
            )
        }
        Events::Live(subscription) => subscription,
    };
    if let Some((run, joined)) = subscription.joined_at() {
        signal(
            stream,
            "oap-subscribed",
            &serde_json::json!({
                "run_id": run.as_str(),
                "joined_after": joined,
                "message": "the subscription begins after this sequence; resubscribe with a cursor at or before it to replay what preceded this point",
            }),
        )?;
    }
    let canceller = subscription.canceller();
    let watched = stream.try_clone()?;
    std::thread::spawn(move || {
        let _ = watched.set_read_timeout(None);
        let mut sink = [0u8; 512];
        loop {
            match (&watched).read(&mut sink) {
                Ok(0) | Err(_) => break,
                Ok(_) => {}
            }
        }
        canceller.cancel();
    });
    loop {
        let next = crate::live::block_on(std::future::poll_fn(|context| {
            futures_core::Stream::poll_next(std::pin::Pin::new(&mut subscription), context)
        }));
        match next {
            Some(Ok(envelope)) => {
                let mut frame = String::new();
                if let Some(sequence) = envelope.sequence {
                    frame.push_str(&format!("id: {sequence}\n"));
                }
                frame.push_str(&format!("data: {}\n\n", serde_json::to_string(&envelope)?));
                stream.write_all(frame.as_bytes())?;
                stream.flush()?;
            }
            Some(Err(crate::HubError::Overflow {
                run_id,
                last_sequence,
            })) => {
                return signal(
                    stream,
                    "oap-overflow",
                    &serde_json::json!({
                        "run_id": run_id.as_str(),
                        "last_sequence": last_sequence,
                        "message": "event stream consumer fell behind; reconnect with a cursor after this sequence",
                    }),
                )
            }
            _ => return Ok(()),
        }
    }
}

fn respond(mut stream: &TcpStream, answer: &Answer, with_body: bool) -> std::io::Result<()> {
    let mut head = format!("HTTP/1.1 {} {}\r\n", answer.status, reason(answer.status));
    if !answer.content_type.is_empty() {
        head.push_str(&format!("Content-Type: {}\r\n", answer.content_type));
    }
    if answer.status != 204 {
        head.push_str(&format!("Content-Length: {}\r\n", answer.body.len()));
    }
    head.push_str("Connection: close\r\n");
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

#[cfg(test)]
mod tests {
    use super::{accepted, Accept};
    use std::io::{Error, ErrorKind};

    #[test]
    fn a_refusal_carries_the_status_go_answers_it_with() {
        for (code, status) in [
            ("unknown_session", 404),
            ("no_run_to_resume", 409),
            ("open_failed", 502),
            ("tools_failed", 502),
            ("resolution_rejected", 409),
            ("probe_failed", 500),
            ("invalid_request", 400),
        ] {
            assert_eq!(super::status_of(code), status, "{code}");
        }
        assert_eq!(super::reason(502), "Bad Gateway");
    }

    #[test]
    fn an_accept_a_peer_aborted_is_served_past_and_only_a_listener_failure_stops() {
        for kind in [
            ErrorKind::ConnectionAborted,
            ErrorKind::ConnectionReset,
            ErrorKind::Interrupted,
        ] {
            assert_eq!(accepted(&Error::from(kind)), Accept::Again, "{kind:?}");
        }
        assert_eq!(accepted(&Error::from_raw_os_error(24)), Accept::Later);
        assert_eq!(
            accepted(&Error::from(ErrorKind::InvalidInput)),
            Accept::Stop
        );
    }
}
