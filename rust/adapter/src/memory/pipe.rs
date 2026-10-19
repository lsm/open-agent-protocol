use std::collections::VecDeque;
use std::pin::Pin;
use std::sync::{Arc, Mutex, MutexGuard};
use std::task::{Context, Poll, Waker};

use futures_core::Stream;
use oap_protocol::Envelope;

use crate::{Error, EventStream};

type Item = Result<Envelope, Error>;

#[derive(Default)]
struct State {
    queue: VecDeque<Item>,
    closed: bool,
    waker: Option<Waker>,
}

#[derive(Clone, Default)]
pub(super) struct Pipe(Arc<Mutex<State>>);

impl Pipe {
    fn lock(&self) -> MutexGuard<'_, State> {
        self.0
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner())
    }

    pub(super) fn send(&self, item: Item) {
        let mut state = self.lock();
        if state.closed {
            return;
        }
        state.queue.push_back(item);
        if let Some(waker) = state.waker.take() {
            waker.wake();
        }
    }

    pub(super) fn close(&self) {
        let mut state = self.lock();
        state.closed = true;
        if let Some(waker) = state.waker.take() {
            waker.wake();
        }
    }

    pub(super) fn stream(&self) -> EventStream {
        Box::pin(Reader(self.clone()))
    }
}

struct Reader(Pipe);

impl Stream for Reader {
    type Item = Item;

    fn poll_next(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Option<Item>> {
        let mut state = self.0.lock();
        if let Some(item) = state.queue.pop_front() {
            return Poll::Ready(Some(item));
        }
        if state.closed {
            return Poll::Ready(None);
        }
        state.waker = Some(cx.waker().clone());
        Poll::Pending
    }
}
