pub use oap_adapter::jsonrpc::{Dialect, Error, Fault};

pub const DIALECT: Dialect = Dialect::strict("hermes");

pub fn error(fault: Fault) -> Error {
    DIALECT.error(fault)
}
