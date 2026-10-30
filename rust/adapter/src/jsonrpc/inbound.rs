use std::sync::{Arc, Mutex};

use crossbeam_channel::Sender;
use serde::Serialize;
use serde_json::value::RawValue;

use super::{Dialect, Error, ErrorObject, Fault, Message, RequestId};

pub trait Responder: Send + Sync {
    fn send(&self, message: Message) -> Result<(), Error>;
}

pub struct IncomingRequest {
    pub id: RequestId,
    pub method: String,
    pub params: Option<Box<RawValue>>,
    pub trace: Option<Box<RawValue>>,
    dialect: Dialect,
    responder: Arc<dyn Responder>,
    done: Mutex<bool>,
}

impl std::fmt::Debug for IncomingRequest {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("IncomingRequest")
            .field("id", &self.id)
            .field("method", &self.method)
            .finish_non_exhaustive()
    }
}

impl IncomingRequest {
    pub fn new(
        dialect: Dialect,
        id: RequestId,
        method: impl Into<String>,
        params: Option<Box<RawValue>>,
        responder: Arc<dyn Responder>,
    ) -> Self {
        Self {
            id,
            method: method.into(),
            params,
            trace: None,
            dialect,
            responder,
            done: Mutex::new(false),
        }
    }

    pub fn respond(&self, result: &impl Serialize) -> Result<(), Error> {
        let raw = serde_json::value::to_raw_value(result)
            .map_err(|error| self.dialect.error(Fault::Encode(error.to_string())))?;
        self.finish(Message::response(self.id.clone(), raw))
    }

    pub fn respond_error(&self, code: i64, message: &str) -> Result<(), Error> {
        self.respond_error_with(code, message, None)
    }

    pub fn respond_error_with(
        &self,
        code: i64,
        message: &str,
        data: Option<Box<RawValue>>,
    ) -> Result<(), Error> {
        self.finish(Message::error_response(
            self.id.clone(),
            ErrorObject {
                code,
                message: message.into(),
                data,
            },
        ))
    }

    fn finish(&self, message: Message) -> Result<(), Error> {
        let mut done = self
            .done
            .lock()
            .unwrap_or_else(|poison| poison.into_inner());
        if *done {
            return Err(self.dialect.error(Fault::ReverseRequestResolved));
        }
        self.responder.send(message)?;
        *done = true;
        Ok(())
    }
}

#[derive(Clone, Debug)]
pub struct NotificationMessage {
    pub method: String,
    pub params: Option<Box<RawValue>>,
}

#[derive(Debug)]
pub enum Inbound {
    Request(Arc<IncomingRequest>),
    Notification(NotificationMessage),
    Barrier(Sender<()>),
}
