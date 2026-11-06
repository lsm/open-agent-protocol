#![doc = include_str!("../README.md")]

pub mod native;
pub mod process;

mod adapter;
pub use adapter::{pin, Adapter, Config};
