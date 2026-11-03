use std::io::{Read, Write};
use std::net::{SocketAddr, TcpListener, TcpStream};
use std::sync::{Arc, Mutex};

use futures::executor::block_on;
use oap_adapter::{
    Adapter, BoxFuture, Descriptor, EventStream, InteractionResolution, OpenRequest, Recovery,
    ResumeRequest, Session, SubmitRequest, TranscriptLoader, TranscriptRequest,
};
use oap_protocol::{
    MessageSubmitResponse, RunCancelResponse, RunId, SessionState, TranscriptLoadResponse,
};
use oap_serve::http::{serve_http, HttpOptions};
use oap_serve::{Hub, Registry};
use serde_json::Value;

struct Native(Box<dyn Session>);

impl Session for Native {
    fn submit(
        &self,
        request: SubmitRequest,
    ) -> BoxFuture<'_, (MessageSubmitResponse, EventStream)> {
        self.0.submit(request)
    }
    fn state(&self) -> BoxFuture<'_, SessionState> {
        self.0.state()
    }
    fn resolve(&self, resolution: InteractionResolution) -> BoxFuture<'_, ()> {
        self.0.resolve(resolution)
    }
    fn cancel(&self, run_id: RunId) -> BoxFuture<'_, RunCancelResponse> {
        self.0.cancel(run_id)
    }
    fn resume(&self, request: ResumeRequest) -> BoxFuture<'_, (Recovery, EventStream)> {
        self.0.resume(request)
    }
    fn close(&self) -> BoxFuture<'_, ()> {
        self.0.close()
    }
    fn native_session_id(&self) -> Option<String> {
        Some("native-1".into())
    }
}

struct Reading {
    inner: Arc<dyn Adapter>,
    asked: Mutex<Vec<TranscriptRequest>>,
}

impl Adapter for Reading {
    fn probe(&self) -> BoxFuture<'_, Descriptor> {
        self.inner.probe()
    }
    fn open(&self, request: OpenRequest) -> BoxFuture<'_, Box<dyn Session>> {
        Box::pin(async move {
            let inner = self.inner.open(request).await?;
            Ok(Box::new(Native(inner)) as Box<dyn Session>)
        })
    }
    fn transcript_loader(&self) -> Option<&dyn TranscriptLoader> {
        Some(self)
    }
}

impl TranscriptLoader for Reading {
    fn transcript_load(&self, request: TranscriptRequest) -> BoxFuture<'_, TranscriptLoadResponse> {
        self.asked.lock().unwrap().push(request);
        Box::pin(async {
            Ok(TranscriptLoadResponse {
                session_id: "native-1".into(),
                next_cursor: "next".into(),
                ..TranscriptLoadResponse::default()
            })
        })
    }
}

fn started(registry: Registry, adapter: &str) -> SocketAddr {
    let hub = Hub::new(registry);
    block_on(hub.open(
        adapter,
        OpenRequest {
            session_id: "s".into(),
            ..OpenRequest::default()
        },
    ))
    .unwrap();
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let addr = listener.local_addr().unwrap();
    std::thread::spawn(move || serve_http(&hub, listener, &HttpOptions::default()));
    addr
}

fn get(addr: SocketAddr, path: &str) -> (u16, Value) {
    let mut stream = TcpStream::connect(addr).unwrap();
    stream
        .set_read_timeout(Some(std::time::Duration::from_secs(10)))
        .unwrap();
    stream
        .write_all(format!("GET {path} HTTP/1.1\r\nHost: {addr}\r\n\r\n").as_bytes())
        .unwrap();
    let mut out = Vec::new();
    stream.read_to_end(&mut out).unwrap();
    let split = out
        .windows(4)
        .position(|window| window == b"\r\n\r\n")
        .unwrap();
    let status = String::from_utf8_lossy(&out[9..12]).parse().unwrap();
    (status, serde_json::from_slice(&out[split + 4..]).unwrap())
}

fn code(answer: &(u16, Value)) -> (u16, Value) {
    (answer.0, answer.1["payload"]["error"]["code"].clone())
}

#[test]
fn a_transcript_page_reads_through_the_adapter_with_the_query_limit_and_cursor() {
    let adapter = Arc::new(Reading {
        inner: Registry::default_registry().lookup("memory").unwrap(),
        asked: Mutex::default(),
    });
    let mut registry = Registry::new();
    registry.register("reading", adapter.clone()).unwrap();
    let addr = started(registry, "reading");
    let (status, page) = get(addr, "/sessions/s/transcript?limit=7&cursor=c1");
    assert_eq!(status, 200, "{page}");
    assert_eq!(page["session_id"], "s");
    assert_eq!(page["next_cursor"], "next");
    let asked = adapter.asked.lock().unwrap();
    assert_eq!(asked[0].native_id, "native-1");
    assert_eq!(asked[0].limit, 7);
    assert_eq!(asked[0].cursor, "c1");
}

#[test]
fn a_transcript_is_refused_for_an_unknown_session_a_bad_limit_and_an_adapter_without_one() {
    let addr = started(Registry::default_registry(), "memory");
    assert_eq!(
        code(&get(addr, "/sessions/ghost/transcript")),
        (404, "unknown_session".into())
    );
    assert_eq!(
        code(&get(addr, "/sessions/s/transcript?limit=501")),
        (400, "invalid_request".into())
    );
    assert_eq!(
        code(&get(addr, "/sessions/s/transcript?limit=x")),
        (400, "invalid_request".into())
    );
    let refused = get(addr, "/sessions/s/transcript");
    assert_eq!(code(&refused), (400, "unsupported_feature".into()));
    assert_eq!(refused.1["session_id"], "s");
}
