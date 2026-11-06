use std::io::Read;

use futures::executor::block_on;
use oap_adapter::{Adapter as AdapterContract, OpenRequest};
use oap_adapter_acp::{native::McpServer, pin, process::ProcessConfig, Adapter, Config};
use oap_protocol::Participant;
use serde::Deserialize;

#[derive(Default, Deserialize)]
#[serde(default)]
struct Input {
    path: String,
    dir: String,
    servers: Vec<McpServer>,
    participant: String,
    reasoning_level: String,
}

fn main() {
    let mut text = String::new();
    std::io::stdin().read_to_string(&mut text).unwrap();
    let input: Input = serde_json::from_str(&text).unwrap();
    let adapter = match Adapter::new(Config {
        process: ProcessConfig {
            path: input.path,
            dir: input.dir,
            env: Some(Vec::new()),
            ..ProcessConfig::default()
        },
        mcp_servers: input.servers,
    }) {
        Ok(adapter) => adapter,
        Err(error) => {
            println!("{}", serde_json::json!({"configError": error.to_string()}));
            return;
        }
    };
    let descriptor = block_on(adapter.probe()).unwrap();
    let request = OpenRequest {
        participant: Participant {
            id: input.participant.as_str().into(),
            ..Participant::default()
        },
        reasoning_level: input.reasoning_level.as_str().into(),
        ..OpenRequest::default()
    };
    let admitted = block_on(adapter.admitter().unwrap().admit(&request));
    let opened = block_on(adapter.open(request));
    println!(
        "{}",
        serde_json::json!({
            "descriptor": descriptor.capabilities,
            "revision": descriptor.capability_revision,
            "journalReplay": descriptor.journal.replay,
            "admitError": admitted.err().unwrap().to_string(),
            "openError": opened.err().unwrap().to_string(),
            "corpus": pin().corpus,
        })
    );
}
