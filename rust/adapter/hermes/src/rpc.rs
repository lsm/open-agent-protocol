pub use oxap_adapter::jsonrpc::{Dialect, Error, Fault};

pub const DIALECT: Dialect = Dialect {
    params_object_or_null: true,
    nonempty_string_ids: true,
    ..Dialect::strict("hermes")
};

pub fn error(fault: Fault) -> Error {
    DIALECT.error(fault)
}
