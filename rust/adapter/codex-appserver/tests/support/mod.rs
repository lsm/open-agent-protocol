#![allow(dead_code)]

use std::collections::VecDeque;
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

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
    inbound: (Mutex<Option<Sender<Inbound>>>, Receiver<Inbound>),
    done: (Sender<()>, Receiver<()>),
    activated: AtomicBool,
    barriers: AtomicBool,
    ahead: Mutex<Vec<Inbound>>,
}

impl Fake {
    pub fn new(replies: impl IntoIterator<Item = Reply>) -> Arc<Self> {
        Arc::new(Self {
            replies: Mutex::new(replies.into_iter().collect()),
            calls: Mutex::default(),
            closes: AtomicUsize::new(0),
            failed: Mutex::default(),
            inbound: {
                let (sender, receiver) = crossbeam_channel::unbounded();
                (Mutex::new(Some(sender)), receiver)
            },
            done: crossbeam_channel::unbounded(),
            activated: AtomicBool::new(false),
            barriers: AtomicBool::new(false),
            ahead: Mutex::default(),
        })
    }

    pub fn calls(&self) -> Vec<(String, String)> {
        self.calls.lock().unwrap().clone()
    }

    pub fn notify(&self, method: &str, params: &str) {
        self.deliver(Inbound::Notification(NotificationMessage {
            method: method.into(),
            params: Some(RawValue::from_string(params.into()).unwrap()),
        }))
        .unwrap();
    }

    pub fn request(&self, method: &str, params: &str) -> Arc<Answers> {
        let (request, answers) = incoming(method, params);
        self.deliver(request).unwrap();
        answers
    }

    pub fn request_ahead_of_next_reply(&self, method: &str, params: &str) -> Arc<Answers> {
        let (request, answers) = incoming(method, params);
        self.ahead.lock().unwrap().push(request);
        answers
    }

    pub fn notify_ahead_of_next_reply(&self, method: &str, params: &str) {
        self.ahead
            .lock()
            .unwrap()
            .push(Inbound::Notification(NotificationMessage {
                method: method.into(),
                params: Some(RawValue::from_string(params.into()).unwrap()),
            }));
    }

    fn deliver(&self, message: Inbound) -> Result<(), ()> {
        self.inbound
            .0
            .lock()
            .unwrap()
            .as_ref()
            .unwrap()
            .send(message)
            .map_err(|_| ())
    }

    pub fn disconnect(&self) {
        self.inbound.0.lock().unwrap().take();
    }

    pub fn hang_up(&self) {
        self.done.0.send(()).unwrap();
    }

    pub fn hold_replies_behind_barriers(&self) {
        self.barriers.store(true, Ordering::SeqCst);
    }

    pub fn activated(&self) -> bool {
        self.activated.load(Ordering::SeqCst)
    }

    fn barrier(&self) -> Result<(), rpc::Error> {
        if !self.barriers.load(Ordering::SeqCst) || !self.activated() {
            return Ok(());
        }
        for message in self.ahead.lock().unwrap().drain(..) {
            self.deliver(message).unwrap();
        }
        let (ack, acked) = crossbeam_channel::bounded::<()>(0);
        self.deliver(Inbound::Barrier(ack)).unwrap();
        match acked.recv_timeout(Duration::from_secs(2)) {
            Err(crossbeam_channel::RecvTimeoutError::Timeout) => Err(error(Fault::Closed)),
            _ => Ok(()),
        }
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
        self.barrier()?;
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
        self.activated.store(true, Ordering::SeqCst);
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

fn incoming(method: &str, params: &str) -> (Inbound, Arc<Answers>) {
    let answers = Arc::new(Answers::default());
    let request = Inbound::Request(Arc::new(IncomingRequest::new(
        DIALECT,
        RequestId::Integer(90),
        method,
        Some(RawValue::from_string(params.into()).unwrap()),
        answers.clone(),
    )));
    (request, answers)
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
