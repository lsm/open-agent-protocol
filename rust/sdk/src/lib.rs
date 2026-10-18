#![cfg_attr(
    not(test),
    deny(
        clippy::unwrap_used,
        clippy::expect_used,
        clippy::panic,
        clippy::indexing_slicing
    )
)]
#![warn(missing_debug_implementations)]

mod agent;
mod auth;
mod binary;
mod client;
mod error;
mod events;
mod execution;
mod ids;
mod models;
mod oap;
mod provider;
mod transport;
mod types;
mod wire;

pub use agent::AgentApi;
pub use auth::{AuthApi, AuthEvent, AuthHandlers, AuthPrompt, ProviderAuthInfo};
pub use binary::BinaryResolver;
pub use client::{Client, ClientBuilder};
pub use error::{AuthErrorKind, Error, Result, StreamErrorKind};
pub use events::{AgentEvent, ProviderEvent};
pub use execution::ExecutionRequest;
pub use models::{
    AuthKind, AuthStatus, ListModelsRequest, ListModelsResponse, ModelCapability, ModelDescriptor,
    ModelLifecycle, ModelSource, ModelsApi, ReasoningLevel,
};
pub use provider::ProviderApi;
pub use types::{
    AuthRetryPolicy, ChatMessage, CompletionResponse, Content, ContentPart, ReasoningEffort, Role,
    RunOptions, Tool, ToolInvocation, ToolResult, Usage,
};
pub use wire::Frame;

#[cfg(doctest)]
#[doc = include_str!("../README.md")]
pub struct Readme;
