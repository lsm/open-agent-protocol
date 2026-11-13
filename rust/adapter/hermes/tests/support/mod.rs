#![allow(dead_code)]

use std::collections::VecDeque;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use crossbeam_channel::{Receiver, Sender};
use oap_adapter::jsonrpc::{Message, Responder};
use oap_adapter::runtime::Runtime;
use oap_adapter::{Error, OpenRequest};
use oap_adapter_hermes::adapter::{Adapter, Config};
use oap_adapter_hermes::client::{Client, ClientFactory};
use oap_adapter_hermes::native::SessionResumeResult;
use oap_adapter_hermes::rpc::{
    self, error, Fault, Inbound, IncomingRequest, NotificationMessage, RequestId, DIALECT,
};
use oap_adapter_hermes::session::Session;
use oap_protocol::{
    Message as OapMessage, MessageContent, MessageRole, MessageSubmitRequest, Participant,
    RequestedDeliveryMode,
};
use serde_json::value::RawValue;

pub type Reply = Result<&'static str, &'static str>;

pub struct Fake {
    replies: Mutex<VecDeque<Reply>>,
    calls: Mutex<Vec<(String, String)>>,
    closes: AtomicUsize,
    pub close_error: Mutex<Option<String>>,
    pub stored: String,
    sender: Mutex<Option<Sender<Inbound>>>,
    inbound: Receiver<Inbound>,
    done: (Sender<()>, Receiver<()>),
    held: Mutex<Option<Receiver<()>>>,
    ahead: Mutex<Vec<String>>,
}

impl Fake {
    pub fn new(replies: impl IntoIterator<Item = Reply>) -> Arc<Self> {
        Self::stored(replies, "")
    }

    pub fn stored(replies: impl IntoIterator<Item = Reply>, stored: &str) -> Arc<Self> {
        let (sender, inbound) = crossbeam_channel::unbounded();
        Arc::new(Self {
            replies: Mutex::new(replies.into_iter().collect()),
            calls: Mutex::default(),
            closes: AtomicUsize::new(0),
            close_error: Mutex::default(),
            stored: stored.into(),
            sender: Mutex::new(Some(sender)),
            inbound,
            done: crossbeam_channel::unbounded(),
            held: Mutex::default(),
            ahead: Mutex::default(),
        })
    }

    pub fn reply(&self, reply: Reply) {
        self.replies.lock().unwrap().push_back(reply);
    }

    pub fn hold_next_reply(&self) -> Sender<()> {
        let (release, held) = crossbeam_channel::bounded(1);
        *self.held.lock().unwrap() = Some(held);
        release
    }

    pub fn event_ahead_of_next_reply(&self, kind: &str, seq: i64, payload: &str) {
        self.ahead
            .lock()
            .unwrap()
            .push(frame("sess0001", kind, seq, payload));
    }

    pub fn count(&self, method: &str) -> usize {
        self.calls()
            .iter()
            .filter(|(name, _)| name == method)
            .count()
    }

    pub fn await_call(&self, method: &str, count: usize) {
        let deadline = Instant::now() + Duration::from_secs(2);
        while self.count(method) < count {
            assert!(
                Instant::now() < deadline,
                "native call {method} was not issued"
            );
            std::thread::sleep(Duration::from_millis(1));
        }
    }

    pub fn event(&self, kind: &str, seq: i64, payload: &str) {
        self.raw(&frame("sess0001", kind, seq, payload));
    }

    pub fn raw(&self, params: &str) {
        self.send(Inbound::Notification(NotificationMessage {
            method: "event".into(),
            params: Some(RawValue::from_string(params.into()).unwrap()),
        }));
        self.barrier();
    }

    pub fn request(&self, id: RequestId, method: &str, params: &str) -> Arc<Answers> {
        let answers = Arc::new(Answers::default());
        self.send(Inbound::Request(Arc::new(IncomingRequest::new(
            DIALECT,
            id,
            method,
            Some(RawValue::from_string(params.into()).unwrap()),
            answers.clone(),
        ))));
        self.barrier();
        answers
    }

    fn send(&self, message: Inbound) {
        let sender = self.sender.lock().unwrap().clone();
        sender
            .expect("the transport is closed")
            .send(message)
            .unwrap();
    }

    pub fn barrier(&self) {
        let (ack, acked) = crossbeam_channel::bounded::<()>(0);
        self.send(Inbound::Barrier(ack));
        assert!(
            matches!(
                acked.recv_timeout(Duration::from_secs(2)),
                Err(crossbeam_channel::RecvTimeoutError::Disconnected)
            ),
            "reducer barrier timed out"
        );
    }

    pub fn hang_up(&self) {
        self.sender.lock().unwrap().take();
        let _ = self.done.0.send(());
    }

    pub fn calls(&self) -> Vec<(String, String)> {
        self.calls.lock().unwrap().clone()
    }

    pub fn closes(&self) -> usize {
        self.closes.load(Ordering::SeqCst)
    }
}

impl Client for Fake {
    fn call(
        &self,
        method: &str,
        params: Box<RawValue>,
        _: Option<Instant>,
    ) -> Result<Box<RawValue>, rpc::Error> {
        self.calls
            .lock()
            .unwrap()
            .push((method.into(), params.get().into()));
        let held = self.held.lock().unwrap().take();
        if let Some(held) = held {
            let _ = held.recv();
        }
        let ahead = std::mem::take(&mut *self.ahead.lock().unwrap());
        for params in ahead {
            self.raw(&params);
        }
        match self.replies.lock().unwrap().pop_front() {
            Some(Ok(result)) => Ok(RawValue::from_string(result.into()).unwrap()),
            Some(Err(message)) => Err(error(Fault::Transport(message.into()))),
            None => Err(error(Fault::Transport(format!(
                "fake client: no scripted reply for {method}"
            )))),
        }
    }

    fn inbound(&self) -> Receiver<Inbound> {
        self.inbound.clone()
    }

    fn done(&self) -> Receiver<()> {
        self.done.1.clone()
    }

    fn err(&self) -> Option<String> {
        Some("EOF".into())
    }

    fn close(&self) -> Result<(), String> {
        self.closes.fetch_add(1, Ordering::SeqCst);
        self.close_error.lock().unwrap().clone().map_or(Ok(()), Err)
    }

    fn stored_session_id(&self) -> String {
        self.stored.clone()
    }
}

pub struct Factory {
    pub client: Arc<Fake>,
    pub native_id: String,
    pub starts: AtomicUsize,
    pub efforts: Mutex<Vec<String>>,
    pub reopened: Mutex<Vec<String>>,
    pub resumed: Option<Result<&'static str, &'static str>>,
}

impl Factory {
    pub fn new(client: &Arc<Fake>, native_id: &str) -> Arc<Self> {
        Arc::new(Self {
            client: client.clone(),
            native_id: native_id.into(),
            starts: AtomicUsize::new(0),
            efforts: Mutex::default(),
            reopened: Mutex::default(),
            resumed: None,
        })
    }

    pub fn reopening(client: &Arc<Fake>, resumed: Result<&'static str, &'static str>) -> Arc<Self> {
        Arc::new(Self {
            resumed: Some(resumed),
            ..Arc::into_inner(Self::new(client, "")).unwrap()
        })
    }
}

impl ClientFactory for Factory {
    fn start(&self, _: Option<Instant>) -> Result<(Arc<dyn Client>, String), Error> {
        self.starts.fetch_add(1, Ordering::SeqCst);
        Ok((self.client.clone(), self.native_id.clone()))
    }

    fn start_with_reasoning(
        &self,
        effort: &str,
        deadline: Option<Instant>,
    ) -> Result<(Arc<dyn Client>, String), Error> {
        self.efforts.lock().unwrap().push(effort.into());
        self.start(deadline)
    }

    fn reopen(
        &self,
        stored: &str,
        _: Option<Instant>,
    ) -> Result<(Arc<dyn Client>, SessionResumeResult), String> {
        self.reopened.lock().unwrap().push(stored.into());
        match self.resumed {
            Some(Ok(result)) => Ok((
                self.client.clone(),
                oap_adapter::gojson::decode(result.as_bytes()).unwrap(),
            )),
            Some(Err(detail)) => Err(detail.into()),
            None => panic!("reopen was not scripted"),
        }
    }
}

pub fn frame(session: &str, kind: &str, seq: i64, payload: &str) -> String {
    let mut params = format!(r#"{{"type":"{kind}","session_id":"{session}","seq":{seq}"#);
    if session.is_empty() {
        params = format!(r#"{{"type":"{kind}""#);
    }
    if !payload.is_empty() {
        params.push_str(&format!(r#","payload":{payload}"#));
    }
    params + "}"
}

pub type Hook = Box<dyn FnOnce() + Send>;

#[derive(Default)]
pub struct Answers {
    pub sent: Mutex<Vec<Message>>,
    pub before: Mutex<Option<Hook>>,
    pub failure: Mutex<Option<String>>,
}

impl Answers {
    pub fn results(&self) -> Vec<String> {
        let answers = self.sent.lock().unwrap();
        answers
            .iter()
            .filter_map(|message| {
                message
                    .result
                    .as_ref()
                    .map(|result| result.get().to_owned())
            })
            .collect()
    }

    pub fn codes(&self) -> Vec<i64> {
        let answers = self.sent.lock().unwrap();
        answers
            .iter()
            .map(|message| message.error.as_ref().map_or(0, |error| error.code))
            .collect()
    }
}

impl Responder for Answers {
    fn send(&self, message: Message) -> Result<(), rpc::Error> {
        let before = self.before.lock().unwrap().take();
        if let Some(before) = before {
            before();
        }
        if let Some(failure) = self.failure.lock().unwrap().clone() {
            return Err(error(Fault::Transport(failure)));
        }
        self.sent.lock().unwrap().push(message);
        Ok(())
    }
}

pub fn go_runtime() -> Runtime {
    let ids = AtomicUsize::new(0);
    let clock = AtomicUsize::new(0);
    Runtime::new(
        move || clock.fetch_add(1, Ordering::SeqCst) as i64 + 1,
        move |kind| {
            let n = ids.fetch_add(1, Ordering::SeqCst);
            format!("{kind}-{}", (b'a' + n as u8) as char)
        },
    )
}

pub fn go_adapter(fake: &Arc<Fake>) -> Adapter {
    Adapter::new(Config {
        factory: Some(Factory::new(fake, "sess0001")),
        model: "hermes-test".into(),
        journal_capacity: 64,
        runtime: go_runtime(),
        ..Config::default()
    })
    .unwrap()
}

pub fn opened(fake: &Arc<Fake>) -> Session {
    go_adapter(fake)
        .open(OpenRequest {
            session_id: "session".into(),
            ..request()
        })
        .unwrap()
}

pub fn submission() -> MessageSubmitRequest {
    MessageSubmitRequest {
        session_id: "session".into(),
        delivery: RequestedDeliveryMode::AUTO,
        messages: vec![OapMessage {
            role: MessageRole::USER,
            content: MessageContent::from_text("hello"),
            ..OapMessage::default()
        }],
        ..MessageSubmitRequest::default()
    }
}

pub fn wait_unusable(session: &Session) {
    let deadline = Instant::now() + Duration::from_secs(2);
    while session.state().is_ok() {
        assert!(Instant::now() < deadline, "the session stayed usable");
        std::thread::sleep(Duration::from_millis(1));
    }
}

pub struct Plain(pub Arc<Fake>);

impl ClientFactory for Plain {
    fn start(&self, _: Option<Instant>) -> Result<(Arc<dyn Client>, String), Error> {
        Ok((self.0.clone(), "sess0001".into()))
    }
}

pub fn adapter(factory: Arc<dyn ClientFactory>) -> Adapter {
    Adapter::new(Config {
        factory: Some(factory),
        model: "hermes-test".into(),
        journal_capacity: 16,
        runtime: Runtime::new(|| 42, |kind| format!("{kind}-minted")),
        ..Config::default()
    })
    .unwrap()
}

pub fn request() -> OpenRequest {
    OpenRequest {
        session_id: "s".into(),
        participant: Participant {
            id: "user".into(),
            ..Participant::default()
        },
        ..OpenRequest::default()
    }
}
