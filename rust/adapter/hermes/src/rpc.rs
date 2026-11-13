pub use oxap_adapter::jsonrpc::{
    ClientOptions, Dialect, Error, Fault, Inbound, NotificationMessage, RpcClient,
};
use serde_json::value::RawValue;

pub const DIALECT: Dialect = Dialect {
    params_object_or_null: true,
    nonempty_string_ids: true,
    ..Dialect::strict("hermes")
};

pub fn error(fault: Fault) -> Error {
    DIALECT.error(fault)
}

pub fn check_notification(method: &str, params: Option<&RawValue>) -> Result<(), String> {
    let params = params.map_or("", |raw| raw.get());
    crate::native::decode_notification(method, params.as_bytes()).map(drop)
}
