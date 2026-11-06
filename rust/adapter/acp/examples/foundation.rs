use std::io::Read;

use futures::executor::block_on;
use oap_adapter::{Adapter as AdapterContract, OpenRequest};
use oap_adapter_acp::{native::McpServer, pin, process::ProcessConfig, Adapter, Config};
use oap_protocol::{ModelsRequest, Participant, FEATURE_MODELS_LIST};
use serde::Deserialize;

#[derive(Default, Deserialize)]
#[serde(default)]
struct Input {
    path: String,
    dir: String,
    servers: Vec<McpServer>,
    participant: String,
    reasoning_level: String,
    open: bool,
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
            shutdown_timeout: Some(std::time::Duration::from_millis(300)),
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
    if input.open {
        let session = match block_on(adapter.open(request)) {
            Ok(session) => session,
            Err(error) => {
                println!("{}", serde_json::json!({"openError": error.to_string()}));
                return;
            }
        };
        let state = block_on(session.state()).unwrap();
        let catalog = block_on(session.model_lister().unwrap().models(ModelsRequest {
            session_id: state.session_id.clone(),
            allow_degraded_features: vec![FEATURE_MODELS_LIST.into()],
        }));
        let closed = block_on(session.close());
        println!(
            "{}",
            serde_json::json!({
                "state": state, "nativeId": session.native_session_id(),
                "models": catalog.as_ref().ok().map(|catalog| &catalog.models),
                "modelError": catalog.err().map(|error| error.to_string()),
                "revision": descriptor.capability_revision,
                "closeError": closed.err().map(|error| error.to_string()),
                "closedStateError": block_on(session.state()).err().unwrap().to_string(),
                "secondCloseError": block_on(session.close()).err().map(|error| error.to_string()),
            })
        );
        return;
    }
    let opened = admitted
        .as_ref()
        .err()
        .map(|_| block_on(adapter.open(request)).err().unwrap().to_string());
    println!(
        "{}",
        serde_json::json!({
            "descriptor": descriptor.capabilities,
            "revision": descriptor.capability_revision,
            "journalReplay": descriptor.journal.replay,
            "admitError": admitted.err().map(|error| error.to_string()),
            "openError": opened,
            "corpus": pin().corpus,
        })
    );
}
