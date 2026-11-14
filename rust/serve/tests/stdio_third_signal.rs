mod support;

use std::io::{BufReader, Read};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use futures::executor::block_on;
use oap_adapter::memory::{Config, Memory};
use oap_adapter::{
    Adapter, BoxFuture, Descriptor, EventStream, InteractionResolution, OpenRequest, Recovery,
    ResumeRequest, Session, SubmitRequest,
};
use oap_protocol::{MessageSubmitResponse, RunCancelResponse, RunId, SessionState};
use oap_serve::stdio::{serve_signalled, Signals, StdioOptions};
use oap_serve::{Hub, Registry};

struct Gate {
    armed: AtomicBool,
    entered: Mutex<Option<mpsc::Sender<()>>>,
    release: Mutex<Option<mpsc::Receiver<()>>>,
}

struct Parking {
    inner: Memory,
    gate: Arc<Gate>,
}

struct ParkedSession {
    inner: Box<dyn Session>,
    gate: Arc<Gate>,
}

impl Adapter for Parking {
    fn probe(&self) -> BoxFuture<'_, Descriptor> {
        self.inner.probe()
    }

    fn open(&self, request: OpenRequest) -> BoxFuture<'_, Box<dyn Session>> {
        let gate = self.gate.clone();
        Box::pin(async move {
            let inner = self.inner.open(request).await?;
            Ok(Box::new(ParkedSession { inner, gate }) as Box<dyn Session>)
        })
    }
}

impl Session for ParkedSession {
    fn submit(
        &self,
        request: SubmitRequest,
    ) -> BoxFuture<'_, (MessageSubmitResponse, EventStream)> {
        self.inner.submit(request)
    }

    fn state(&self) -> BoxFuture<'_, SessionState> {
        if self.gate.armed.load(Ordering::SeqCst) {
            if let Some(entered) = self.gate.entered.lock().unwrap().take() {
                let _ = entered.send(());
            }
            if let Some(release) = self.gate.release.lock().unwrap().take() {
                let _ = release.recv();
            }
        }
        self.inner.state()
    }

    fn resolve(&self, resolution: InteractionResolution) -> BoxFuture<'_, ()> {
        self.inner.resolve(resolution)
    }

    fn cancel(&self, run_id: RunId) -> BoxFuture<'_, RunCancelResponse> {
        self.inner.cancel(run_id)
    }

    fn resume(&self, request: ResumeRequest) -> BoxFuture<'_, (Recovery, EventStream)> {
        self.inner.resume(request)
    }

    fn close(&self) -> BoxFuture<'_, ()> {
        self.inner.close()
    }
}

struct Held;

impl Read for Held {
    fn read(&mut self, _: &mut [u8]) -> std::io::Result<usize> {
        loop {
            std::thread::park();
        }
    }
}

#[test]
fn a_third_signal_ends_serving_while_an_earlier_one_is_parked() {
    let (entered, parked) = mpsc::channel();
    let (released, release) = mpsc::channel();
    let gate = Arc::new(Gate {
        armed: AtomicBool::new(false),
        entered: Mutex::new(Some(entered)),
        release: Mutex::new(Some(release)),
    });
    let mut registry = Registry::default_registry();
    registry
        .register(
            "parking",
            Arc::new(Parking {
                inner: Memory::new(Config::default()),
                gate: gate.clone(),
            }),
        )
        .unwrap();
    let hub = Hub::new(registry);
    block_on(hub.open(
        "parking",
        OpenRequest {
            session_id: "s".into(),
            ..OpenRequest::default()
        },
    ))
    .unwrap();
    gate.armed.store(true, Ordering::SeqCst);
    let signals = Signals::new(&hub);
    let (done, served) = mpsc::channel();
    let serving = signals.clone();
    std::thread::spawn(move || {
        let _ = done.send(serve_signalled(
            &serving,
            BufReader::new(Held),
            support::Captured::default(),
            &StdioOptions::default(),
        ));
    });
    let first = signals.clone();
    std::thread::spawn(move || first.signal(1));
    parked
        .recv_timeout(Duration::from_secs(10))
        .expect("the first signal never reached the session");
    assert!(signals.signal(3).is_none());
    let ended = served.recv_timeout(Duration::from_secs(10));
    let _ = released.send(());
    assert!(
        ended.is_ok(),
        "the third signal did not end serving while the first was parked"
    );
}
