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

pub fn fake_binary() -> PathBuf {
    PathBuf::from(env!("CARGO_BIN_EXE_oap-protocol-fake"))
}

pub fn fake_builder(scenario: &str) -> ClientBuilder {
    ClientBuilder::new()
        .command(fake_binary())
        .args(Vec::<String>::new())
        .env_clear()
        .env("OAP_SDK_FAKE_SCENARIO", scenario)
        .handshake_timeout(Duration::from_millis(2_000))
        .response_timeout(Duration::from_millis(2_000))
        .frame_timeout(Duration::from_millis(2_000))
}

pub async fn fake_client(scenario: &str) -> Client {
    fake_builder(scenario)
        .connect()
        .await
        .expect("fake server connects")
}

pub fn real_binary() -> Option<PathBuf> {
    let path = std::env::var("OAP_SDK_BINARY_PATH").ok()?;
    let path = PathBuf::from(path);
    path.exists().then_some(path)
}

const SMOKE_PROVIDERS: &str = r#"{"providers":[
{"id":"smoke","name":"Smoke","api":"openai-completions","base_url":"http://127.0.0.1:9/v1","auth":"none","models":["smoke-model"]},
{"id":"smoke-keyed","name":"Smoke Keyed","api":"openai-completions","base_url":"http://127.0.0.1:9/v1","auth":{"env":"OAP_SMOKE_KEY_THAT_IS_NEVER_SET"},"models":["keyed-model"]}]}"#;

static SMOKE_HOMES: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);

pub fn smoke_home() -> PathBuf {
    let index = SMOKE_HOMES.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
    let home = std::env::temp_dir().join(format!("oapx-rust-sdk-{}-{index}", std::process::id()));
    let dir = home.join(".oapx");
    std::fs::create_dir_all(&dir).expect("smoke home");
    std::fs::write(dir.join("providers.json"), SMOKE_PROVIDERS).expect("providers.json");
    home
}

pub fn real_builder(path: &std::path::Path) -> ClientBuilder {
    let mut builder = ClientBuilder::new()
        .command(path)
        .env_clear()
        .handshake_timeout(Duration::from_millis(5_000))
        .response_timeout(Duration::from_millis(20_000))
        .frame_timeout(Duration::from_millis(20_000));
    for key in ["PATH", "TMPDIR", "USER"] {
        if let Ok(value) = std::env::var(key) {
            builder = builder.env(key, value);
        }
    }
    let home = smoke_home().display().to_string();
    builder = builder.env("HOME", &home).env("USERPROFILE", &home);
    builder.env("OAPX_KEYCHAIN_SERVICE", "com.makai.auth.rust-sdk-tests")
}

pub fn real_builder_with_fixture(path: &std::path::Path) -> ClientBuilder {
    real_builder(path).env("OAPX_TEST_FIXTURE_PROVIDER", "1")
}

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
