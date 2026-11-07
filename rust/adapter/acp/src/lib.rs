#![doc = include_str!("../README.md")]

pub mod native;
pub mod process;

mod adapter;
pub use adapter::{pin, Adapter, Config};

mod operation;
pub use operation::Operation;

mod session;

mod models;

mod prompt;
pub use prompt::{PromptBridge, PromptCall};

mod text;
pub use text::{TextRun, TextRunContext, TextRunState};

mod submit;

mod usage;
pub use usage::UsageScope;
