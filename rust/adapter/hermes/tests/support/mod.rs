#![allow(dead_code)]

use std::collections::VecDeque;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Instant;

use crossbeam_channel::{Receiver, Sender};
use oap_adapter::runtime::Runtime;
use oap_adapter::{Error, OpenRequest};
use oap_adapter_hermes::adapter::{Adapter, Config};
use oap_adapter_hermes::client::{Client, ClientFactory};
use oap_adapter_hermes::native::SessionResumeResult;
use oap_adapter_hermes::rpc::{self, error, Fault, Inbound};
use oap_protocol::Participant;
use serde_json::value::RawValue;

pub type Reply = Result<&'static str, &'static str>;

pub struct Fake {
    replies: Mutex<VecDeque<Reply>>,
    calls: Mutex<Vec<(String, String)>>,
    closes: AtomicUsize,
    pub close_error: Mutex<Option<String>>,
    pub stored: String,
    inbound: (Sender<Inbound>, Receiver<Inbound>),
    done: (Sender<()>, Receiver<()>),
}

impl Fake {
    pub fn new(replies: impl IntoIterator<Item = Reply>) -> Arc<Self> {
        Self::stored(replies, "")
    }

    pub fn stored(replies: impl IntoIterator<Item = Reply>, stored: &str) -> Arc<Self> {
        Arc::new(Self {
            replies: Mutex::new(replies.into_iter().collect()),
            calls: Mutex::default(),
            closes: AtomicUsize::new(0),
            close_error: Mutex::default(),
            stored: stored.into(),
            inbound: crossbeam_channel::unbounded(),
            done: crossbeam_channel::unbounded(),
        })
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
        match self.replies.lock().unwrap().pop_front() {
            Some(Ok(result)) => Ok(RawValue::from_string(result.into()).unwrap()),
            Some(Err(message)) => Err(error(Fault::Transport(message.into()))),
            None => Err(error(Fault::Transport(format!(
                "fake client: no scripted reply for {method}"
            )))),
        }
    }

    fn inbound(&self) -> Receiver<Inbound> {
        self.inbound.1.clone()
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
