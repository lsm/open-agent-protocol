pub use oap_adapter::jsonrpc::{
    Dialect, Error, ErrorObject, Fault, Inbound, IncomingRequest, Kind, Message,
    NotificationMessage, RequestId, Responder,
};

pub const DIALECT: Dialect = Dialect::unversioned("codex app-server");

pub fn error(fault: Fault) -> Error {
    DIALECT.error(fault)
}
