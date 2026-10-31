pub use oap_adapter::jsonrpc::{
    ClientOptions, Dialect, Error, Fault, Inbound, RpcClient, Started, DEFAULT_FRAME_LIMIT,
};
use serde_json::value::RawValue;

pub const DIALECT: Dialect = Dialect::strict("deepseek");

pub fn error(fault: Fault) -> Error {
    DIALECT.error(fault)
}

pub fn check_notification(method: &str, params: Option<&RawValue>) -> Result<(), String> {
    let params = params.map_or("", |raw| raw.get());
    crate::native::decode_notification(method, params.as_bytes()).map(drop)
}
