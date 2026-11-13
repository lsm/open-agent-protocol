use std::sync::Arc;

use crossbeam_channel::select;
use oap_adapter::gojson::go_quote;

use crate::native::{
    decode_notification, is_run_scoped, request_session_id, Event, REQUEST_APPROVAL,
    REQUEST_CLARIFY, REQUEST_SECRET, REQUEST_SUDO,
};
use crate::rpc::{Inbound, IncomingRequest, RequestId};
use crate::session::{Inner, Shared};

impl Shared {
    pub(crate) fn dispatch(&self) {
        let (inbound, done, stop) = (self.client.inbound(), self.client.done(), &self.stopped);
        let mut hung_up = false;
        loop {
            if hung_up {
                select! {
                    recv(inbound) -> message => match message {
                        Ok(message) => self.reduce(message),
                        Err(_) => return self.transport_failed(),
                    },
                    recv(stop) -> _ => return,
                }
            } else {
                select! {
                    recv(inbound) -> message => match message {
                        Ok(message) => self.reduce(message),
                        Err(_) => select! {
                            recv(done) -> _ => return self.transport_failed(),
                            recv(stop) -> _ => return,
                        },
                    },
                    recv(done) -> _ => hung_up = true,
                    recv(stop) -> _ => return,
                }
            }
        }
    }

    fn reduce(&self, message: Inbound) {
        match message {
            Inbound::Barrier(ack) => drop(ack),
            Inbound::Request(request) => self.apply_request(&mut self.lock(), request),
            Inbound::Notification(notification) => {
                let params = notification.params.as_deref().map_or("", |raw| raw.get());
                let mut inner = self.lock();
                match decode_notification(&notification.method, params.as_bytes()) {
                    Ok(event) => self.apply_event(&mut inner, event),
                    Err(_) => self.foreign_activity(&mut inner, "non-event notification"),
                }
            }
        }
    }

    fn apply_event(&self, inner: &mut Inner, event: Event) {
        if event.session_id.is_empty() {
            return;
        }
        if event.session_id != self.native_id {
            let what = format!("event for foreign session {}", go_quote(&event.session_id));
            return self.foreign_activity(inner, &what);
        }
        if event.seq != inner.last_seq + 1 {
            let what = format!("non-contiguous seq {} after {}", event.seq, inner.last_seq);
            return self.foreign_activity(inner, &what);
        }
        inner.last_seq = event.seq;
        if inner.unusable {
            return;
        }
        if is_run_scoped(&event.kind) {
            let what = format!(
                "session event {} without a reserved run",
                go_quote(&event.kind)
            );
            self.foreign_activity(inner, &what);
        }
    }

    fn apply_request(&self, inner: &mut Inner, request: Arc<IncomingRequest>) {
        let method = request.method.as_str();
        if !matches!(request.id, RequestId::String(_)) {
            let what = format!("{method} request with a non-string id");
            return self.foreign_activity(inner, &what);
        }
        if !matches!(
            method,
            REQUEST_APPROVAL | REQUEST_CLARIFY | REQUEST_SUDO | REQUEST_SECRET
        ) {
            let _ = request.respond_error(-32601, "method not found");
            return;
        }
        let params = request.params.as_deref().map_or("", |raw| raw.get());
        let session = request_session_id(params.as_bytes());
        if session != self.native_id {
            let what = format!(
                "{method} request for foreign session {}",
                go_quote(&session)
            );
            return self.foreign_activity(inner, &what);
        }
        if inner.unusable {
            return;
        }
        let what = format!("{method} request without an owned run");
        self.foreign_activity(inner, &what);
    }

    fn foreign_activity(&self, inner: &mut Inner, _what: &str) {
        inner.unusable = true;
    }

    fn transport_failed(&self) {
        let mut inner = self.lock();
        if !inner.closed {
            inner.unusable = true;
        }
    }
}
