use std::collections::VecDeque;
use std::pin::Pin;
use std::sync::{Arc, Mutex};
use std::task::{Context, Poll, Waker};

use crate::{Error, EventStream};
use futures_core::Stream;
use oap_protocol::Envelope;

struct Queue {
    capacity: usize,
    items: VecDeque<Result<Envelope, Error>>,
    closed: bool,
    waker: Option<Waker>,
}

#[derive(Clone)]
pub struct Subscriber(Arc<Mutex<Queue>>);

impl Subscriber {
    pub fn new(capacity: usize) -> Self {
        Self(Arc::new(Mutex::new(Queue {
            capacity,
            items: VecDeque::new(),
            closed: false,
            waker: None,
        })))
    }

    fn with<T>(&self, apply: impl FnOnce(&mut Queue) -> T) -> T {
        let mut queue = self.0.lock().unwrap_or_else(|poison| poison.into_inner());
        let out = apply(&mut queue);
        if let Some(waker) = queue.waker.take() {
            waker.wake();
        }
        out
    }

    pub fn offer(&self, envelope: Envelope, terminal: bool) -> bool {
        self.with(|queue| {
            if queue.items.len() < queue.capacity {
                queue.items.push_back(Ok(envelope));
                queue.closed = terminal;
                return !terminal;
            }
            queue.items.push_back(Err(Error::EventStreamOverflow));
            queue.closed = true;
            false
        })
    }

    pub fn replay(&self, envelope: Envelope) {
        self.with(|queue| queue.items.push_back(Ok(envelope)));
    }

    pub fn close(&self) {
        self.with(|queue| queue.closed = true);
    }

    pub fn stream(&self) -> EventStream {
        Box::pin(self.clone())
    }
}

impl Stream for Subscriber {
    type Item = Result<Envelope, Error>;

    fn poll_next(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        let mut queue = self.0.lock().unwrap_or_else(|poison| poison.into_inner());
        if let Some(item) = queue.items.pop_front() {
            return Poll::Ready(Some(item));
        }
        if queue.closed {
            return Poll::Ready(None);
        }
        queue.waker = Some(cx.waker().clone());
        Poll::Pending
    }
}
