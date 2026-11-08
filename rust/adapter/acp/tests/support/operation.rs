use std::cell::RefCell;
use std::future::Future;
use std::pin::Pin;
use std::sync::mpsc;
use std::task::{Context, Poll};
use std::time::Duration;

use futures::task::noop_waker;

use super::Operation;

struct Exit(mpsc::Receiver<()>);

impl Drop for Exit {
    fn drop(&mut self) {
        let _ = self.0.recv();
    }
}

thread_local! {
    static EXIT: RefCell<Option<Exit>> = const { RefCell::new(None) };
}

#[test]
fn a_ready_operation_does_not_wait_for_its_worker_to_exit() {
    let (release, held) = mpsc::channel();
    let mut operation = Operation::spawn(move |_| {
        EXIT.with(|exit| *exit.borrow_mut() = Some(Exit(held)));
        Ok(())
    });
    let (done, finished) = mpsc::channel();
    let poller = std::thread::spawn(move || loop {
        if let Poll::Ready(outcome) =
            Pin::new(&mut operation).poll(&mut Context::from_waker(&noop_waker()))
        {
            let _ = done.send(outcome.is_ok());
            return;
        }
        std::thread::yield_now();
    });
    let ready = finished.recv_timeout(Duration::from_secs(5));
    release.send(()).unwrap();
    poller.join().unwrap();
    assert_eq!(ready, Ok(true));
}
