//! Shared helpers for the integration tests.

#![allow(
    clippy::unwrap_used,
    clippy::expect_used,
    clippy::panic,
    clippy::indexing_slicing,
    dead_code
)]

use std::path::PathBuf;
use std::time::Duration;

use oap_sdk::{Client, ClientBuilder};

/// The OAP protocol fake built alongside the crate.
pub fn fake_binary() -> PathBuf {
    PathBuf::from(env!("CARGO_BIN_EXE_oap-protocol-fake"))
}

/// A client wired to the protocol fake running `scenario`.
pub fn fake_builder(scenario: &str) -> ClientBuilder {
    // `env_clear` keeps an ambient `OAP_SDK_BINARY_PATH` or a stale scenario from
    // the developer's shell out of the child, so a test means exactly one thing.
    ClientBuilder::new()
        .command(fake_binary())
        .args(Vec::<String>::new())
        .env_clear()
        .env("OAP_SDK_FAKE_SCENARIO", scenario)
        .handshake_timeout(Duration::from_millis(2_000))
        .response_timeout(Duration::from_millis(2_000))
        .frame_timeout(Duration::from_millis(2_000))
}

/// Connects a client to the protocol fake running `scenario`.
pub async fn fake_client(scenario: &str) -> Client {
    fake_builder(scenario)
        .connect()
        .await
        .expect("fake server connects")
}

/// The real `oapx` binary, when `OAP_SDK_BINARY_PATH` names one.
///
/// Mirrors the TypeScript SDK's smoke tests: without the variable the
/// binary-backed tests skip rather than fail, so `cargo test` is green on a
/// machine with no runtime build.
pub fn real_binary() -> Option<PathBuf> {
    let path = std::env::var("OAP_SDK_BINARY_PATH").ok()?;
    let path = PathBuf::from(path);
    path.exists().then_some(path)
}

/// A client wired to the real runtime.
pub fn real_builder(path: &std::path::Path) -> ClientBuilder {
    let mut builder = ClientBuilder::new()
        .command(path)
        .env_clear()
        .handshake_timeout(Duration::from_millis(5_000))
        .response_timeout(Duration::from_millis(20_000))
        .frame_timeout(Duration::from_millis(20_000));
    for key in ["HOME", "PATH", "TMPDIR", "USER"] {
        if let Ok(value) = std::env::var(key) {
            builder = builder.env(key, value);
        }
    }
    // On macOS the runtime reads credentials from the login Keychain before it
    // answers anything auth-aware, and an unsigned local build blocks there
    // waiting on a GUI prompt that a test runner can never satisfy. Naming a
    // service that holds no item makes the lookup miss immediately and fall
    // through to the file store, which is what CI (Linux) uses anyway.
    builder.env("OAPX_KEYCHAIN_SERVICE", "com.makai.auth.rust-sdk-tests")
}

/// A client wired to the real runtime with the CI fixture auth provider asked
/// for by name.
///
/// The runtime serves `test-fixture` only when `OAPX_TEST_FIXTURE_PROVIDER=1`,
/// so a test that logs into it must set that. It is a separate builder rather
/// than another entry in `real_builder` so that the tests which want a plain
/// user keep exercising what a plain user sees, and so the opt-in is visible
/// at every call site.
pub fn real_builder_with_fixture(path: &std::path::Path) -> ClientBuilder {
    real_builder(path).env("OAPX_TEST_FIXTURE_PROVIDER", "1")
}

/// Skips the test body unless a real runtime is available.
#[macro_export]
macro_rules! require_real_binary {
    () => {
        match $crate::common::real_binary() {
            Some(path) => path,
            None => {
                eprintln!("skipping: OAP_SDK_BINARY_PATH is not set");
                return;
            }
        }
    };
}
