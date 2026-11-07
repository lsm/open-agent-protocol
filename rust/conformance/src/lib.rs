#[cfg(unix)]
mod process;
#[cfg(unix)]
mod report;
#[cfg(unix)]
mod runner;
mod transcript;
mod wire;

#[cfg(unix)]
pub use process::{Process, ProcessError};
#[cfg(unix)]
pub use report::{run, Options, Report};
#[cfg(unix)]
pub use runner::{Check, Runner};
pub use transcript::Transcript;
pub use wire::{read_frame, write_frame, ControlFrame, Frame, WireError};
#[cfg(unix)]
mod client;
#[cfg(unix)]
mod framing;
#[cfg(unix)]
pub use client::{Client, ClientError, CONTROL_ANSWER_BUDGET, DEFAULT_LINE_DEADLINE};
#[cfg(unix)]
pub use framing::framing_contract;
