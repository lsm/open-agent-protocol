#![doc = include_str!("../README.md")]

pub mod native;
pub mod rpc;

use std::sync::LazyLock;

fn pin() -> &'static oap_harness::Version {
    oap_harness::current("copilot-cli").expect("the harness catalog pins copilot-cli")
}

pub static PINNED_VERSION: LazyLock<String> = LazyLock::new(|| pin().endpoint_version.clone());
pub static CAPABILITY_REVISION: LazyLock<String> =
    LazyLock::new(|| pin().capability_revision.clone());
pub static CORPUS_DIRECTORY: LazyLock<String> = LazyLock::new(|| pin().corpus.clone());
