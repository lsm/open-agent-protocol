mod client;
mod codec;
mod inbound;
mod message;

use std::fmt;

pub use client::{
    ClientOptions, Closer, RpcClient, Started, DEFAULT_QUEUE_CAPACITY, DEFAULT_STALL,
};
pub use codec::{Decoder, Encoder, DEFAULT_FRAME_LIMIT};
pub use inbound::{Inbound, IncomingRequest, NotificationMessage, Responder};
pub use message::{ErrorObject, Kind, Message, RequestId};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Dialect {
    pub name: &'static str,
    pub strict_frames: bool,
    pub limit_writes: bool,
    pub params_object_or_null: bool,
    pub nonempty_string_ids: bool,
}

impl Dialect {
    pub const fn lenient(name: &'static str) -> Self {
        Self {
            name,
            strict_frames: false,
            limit_writes: false,
            params_object_or_null: false,
            nonempty_string_ids: false,
        }
    }

    pub const fn strict(name: &'static str) -> Self {
        Self {
            strict_frames: true,
            limit_writes: true,
            ..Self::lenient(name)
        }
    }

    pub(crate) fn params_shape(&self, raw: &str) -> bool {
        let trimmed = raw.trim();
        if self.params_object_or_null {
            return trimmed == "null" || trimmed.starts_with('{');
        }
        message::object_or_array(trimmed)
    }

    pub(crate) fn params_rule(&self) -> &'static str {
        if self.params_object_or_null {
            "params must be an object or null"
        } else {
            "params must be an object or array"
        }
    }

    pub fn error(&self, fault: Fault) -> Error {
        Error {
            dialect: self.name,
            fault,
        }
    }
}

#[derive(Debug)]
pub enum Fault {
    InvalidMessage(String),
    InvalidId(String),
    FrameTooLarge,
    Closed,
    ReverseRequestResolved,
    Remote { id: RequestId, object: ErrorObject },
    Decode { method: String, detail: String },
    Encode(String),
    DeadlineExceeded,
    Transport(String),
    Io(std::io::Error),
}

#[derive(Debug)]
pub struct Error {
    pub dialect: &'static str,
    pub fault: Fault,
}

fn detail(text: &str) -> String {
    if text.is_empty() {
        String::new()
    } else {
        format!(": {text}")
    }
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let name = self.dialect;
        match &self.fault {
            Fault::InvalidMessage(text) => {
                write!(f, "{name} rpc: invalid JSON-RPC message{}", detail(text))
            }
            Fault::InvalidId(text) => {
                write!(
                    f,
                    "{name} rpc: id must be a string or integer{}",
                    detail(text)
                )
            }
            Fault::FrameTooLarge => write!(f, "{name} rpc: frame exceeds configured limit"),
            Fault::Closed => write!(f, "{name} rpc: client closed"),
            Fault::ReverseRequestResolved => {
                write!(f, "{name} rpc: reverse request already resolved")
            }
            Fault::Remote { id, object } => write!(
                f,
                "{name} rpc error {} for request {id}: {}",
                object.code, object.message
            ),
            Fault::Decode { method, detail } => write!(f, "decode {method} response: {detail}"),
            Fault::Encode(text) | Fault::Transport(text) => f.write_str(text),
            Fault::DeadlineExceeded => f.write_str("context deadline exceeded"),
            Fault::Io(error) => write!(f, "{error}"),
        }
    }
}

impl std::error::Error for Error {}

impl Error {
    pub fn remote_code(&self) -> Option<i64> {
        match &self.fault {
            Fault::Remote { object, .. } => Some(object.code),
            _ => None,
        }
    }
}
