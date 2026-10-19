pub mod codes;
mod diagnostic;
mod schema;
mod state;
mod trace;
mod validator;

pub use diagnostic::{Diagnostic, Phase, ValidationResult};
pub use schema::{SchemaError, SCHEMAS, SCHEMA_BASE};
pub use trace::duplicate_key;
pub use validator::Validator;
