#![allow(dead_code)]

use std::collections::VecDeque;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Instant;

use crossbeam_channel::{Receiver, Sender};
use oap_adapter::runtime::Runtime;
use oap_adapter::OpenRequest;
use oap_adapter_codex_appserver::adapter::{Adapter, Config};
use oap_adapter_codex_appserver::client::{Client, ClientFactory};
use oap_adapter_codex_appserver::process::ProcessConfig;
use oap_adapter_codex_appserver::rpc::{
    self, error, ErrorObject, Fault, Inbound, IncomingRequest, Message, NotificationMessage,
    RequestId, Responder, DIALECT,
};
use oap_protocol::Participant;
use serde_json::value::RawValue;

pub type Reply = Result<&'static str, &'static str>;

pub struct Fake {
    replies: Mutex<VecDeque<Reply>>,
    calls: Mutex<Vec<(String, String)>>,
    closes: AtomicUsize,
    pub failed: Mutex<Option<String>>,
    inbound: (Sender<Inbound>, Receiver<Inbound>),
    done: (Sender<()>, Receiver<()>),
}

impl Fake {
    pub fn new(replies: impl IntoIterator<Item = Reply>) -> Arc<Self> {
        Arc::new(Self {
            replies: Mutex::new(replies.into_iter().collect()),
            calls: Mutex::default(),
            closes: AtomicUsize::new(0),
            failed: Mutex::default(),
            inbound: crossbeam_channel::unbounded(),
            done: crossbeam_channel::unbounded(),
        })
    }

    pub fn calls(&self) -> Vec<(String, String)> {
        self.calls.lock().unwrap().clone()
    }

    pub fn notify(&self, method: &str, params: &str) {
        self.inbound
            .0
            .send(Inbound::Notification(NotificationMessage {
                method: method.into(),
                params: Some(RawValue::from_string(params.into()).unwrap()),
            }))
            .unwrap();
    }

    pub fn request(&self, method: &str, params: &str) -> Arc<Answers> {
        let answers = Arc::new(Answers::default());
        self.inbound
            .0
            .send(Inbound::Request(Arc::new(IncomingRequest::new(
                DIALECT,
                RequestId::Integer(90),
                method,
                Some(RawValue::from_string(params.into()).unwrap()),
                answers.clone(),
            ))))
            .unwrap();
        answers
    }

    pub fn hang_up(&self) {
        self.done.0.send(()).unwrap();
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
            Some(Err(message)) => Err(error(Fault::Remote {
                id: RequestId::Integer(1),
                object: ErrorObject {
                    code: -32600,
                    message: message.into(),
                    data: None,
                },
            })),
            None => Err(error(Fault::Closed)),
        }
    }

    fn inbound(&self) -> Receiver<Inbound> {
        self.inbound.1.clone()
    }

    fn done(&self) -> Receiver<()> {
        self.done.1.clone()
    }

    fn err(&self) -> Option<String> {
        self.failed.lock().unwrap().clone()
    }

    fn close(&self) -> Result<(), rpc::Error> {
        self.closes.fetch_add(1, Ordering::SeqCst);
        Ok(())
    }
}

pub fn adapter(fake: &Arc<Fake>, configure: impl FnOnce(&mut Config)) -> Adapter {
    let client = fake.clone();
    let factory: Arc<dyn ClientFactory> =
        Arc::new(move |_: Option<Instant>| Ok(client.clone() as Arc<dyn Client>));
    let mut config = Config {
        factory: Some(factory),
        process: ProcessConfig {
            dir: "/work".into(),
            ..ProcessConfig::default()
        },
        model: "gpt-test".into(),
        approval_policy: "never".into(),
        sandbox: "read-only".into(),
        runtime: Runtime::new(|| 42, |kind| format!("{kind}-minted")),
        ..Config::default()
    };
    configure(&mut config);
    Adapter::new(config).unwrap()
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

#[derive(Default)]
pub struct Answers(pub Mutex<Vec<Message>>);

impl Responder for Answers {
    fn send(&self, message: Message) -> Result<(), rpc::Error> {
        self.0.lock().unwrap().push(message);
        Ok(())
    }
}
