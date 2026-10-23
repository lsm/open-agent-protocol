use std::collections::VecDeque;
use std::pin::Pin;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::task::{Context, Poll};

use futures::Stream;
use oap_adapter::*;
use oap_adapter::Error;
use oap_protocol::*;

pub struct ScriptSession {
    pub admission: MessageSubmitResponse,
    pub submit_error: Option<Error>,
    pub dropped: Arc<AtomicBool>,
    pub events: Mutex<VecDeque<Result<Envelope, Error>>>,
    pub hold: bool,
}

impl ScriptSession {
    pub fn new(events: Vec<Result<Envelope, Error>>, hold: bool) -> Self {
        Self {
            admission: MessageSubmitResponse {
                accepted: true,
                session_id: "session-1".into(),
                run_id: Some("run-1".into()),
                ..MessageSubmitResponse::default()
            },
            submit_error: None,
            dropped: Arc::new(AtomicBool::new(false)),
            events: Mutex::new(events.into()),
            hold,
        }
    }
}

struct Reader {
    events: VecDeque<Result<Envelope, Error>>,
    dropped: Arc<AtomicBool>,
    hold: bool,
}

impl Drop for Reader {
    fn drop(&mut self) {
        self.dropped.store(true, Ordering::SeqCst);
    }
}

impl Stream for Reader {
    type Item = Result<Envelope, Error>;

    fn poll_next(mut self: Pin<&mut Self>, _: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        if let Some(item) = self.events.pop_front() {
            Poll::Ready(Some(item))
        } else if self.hold {
            Poll::Pending
        } else {
            Poll::Ready(None)
        }
    }
}

impl Session for ScriptSession {
    fn submit(&self, _: SubmitRequest) -> BoxFuture<'_, (MessageSubmitResponse, EventStream)> {
        Box::pin(async move {
            if let Some(error) = &self.submit_error {
                return Err(error.clone());
            }
            let stream: EventStream = Box::pin(Reader {
                events: std::mem::take(&mut *self.events.lock().unwrap()),
                dropped: self.dropped.clone(),
                hold: self.hold,
            });
            Ok((self.admission.clone(), stream))
        })
    }

    fn state(&self) -> BoxFuture<'_, SessionState> {
        Box::pin(async { Err(Error::UnsupportedInput) })
    }

    fn resolve(&self, _: InteractionResolution) -> BoxFuture<'_, ()> {
        Box::pin(async { Err(Error::UnsupportedInput) })
    }

    fn cancel(&self, _: RunId) -> BoxFuture<'_, RunCancelResponse> {
        Box::pin(async { Err(Error::UnsupportedInput) })
    }

    fn resume(&self, _: ResumeRequest) -> BoxFuture<'_, (Recovery, EventStream)> {
        Box::pin(async { Err(Error::UnsupportedInput) })
    }

    fn close(&self) -> BoxFuture<'_, ()> {
        Box::pin(async { Ok(()) })
    }
}
