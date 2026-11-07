use std::io::Read;

use futures::executor::block_on;
use oap_adapter::{Adapter as AdapterContract, Error, OpenRequest};
use oap_adapter_acp::{native::McpServer, pin, process::ProcessConfig, Adapter, Config};
use oap_protocol::{
    ModelsRequest, Participant, SessionModelSwitchRequest, FEATURE_MODELS_LIST,
    FEATURE_SESSION_MODEL_SWITCH,
};
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
    model: Option<String>,
    allow_switch: bool,
    switch_session: Option<String>,
}

fn switch_details(error: &Error) -> serde_json::Value {
    match error {
        Error::UnsupportedControl(control) => serde_json::json!({
            "kind":"unsupported_control", "feature":control.feature, "reason":control.reason,
            "field":control.field, "detail":control.detail,
        }),
        Error::DegradedControl(feature) => {
            serde_json::json!({"kind":"degraded", "feature":feature})
        }
        Error::ModelNotFound(id) => serde_json::json!({"kind":"model_not_found", "id":id}),
        Error::InvalidSubmission => serde_json::json!({"kind":"invalid_submission"}),
        Error::SessionClosed => serde_json::json!({"kind":"session_closed"}),
        _ => serde_json::json!({"kind":"other"}),
    }
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
        ..Config::default()
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
        let mut state = block_on(session.state()).unwrap();
        let initial = state.clone();
        let mut switched = None;
        let mut switch_error = None;
        if let Some(model_id) = input.model {
            match block_on(
                session
                    .model_switcher()
                    .unwrap()
                    .switch_model(SessionModelSwitchRequest {
                        session_id: input
                            .switch_session
                            .map(Into::into)
                            .unwrap_or_else(|| state.session_id.clone()),
                        model_id,
                        allow_degraded_features: if input.allow_switch {
                            vec![FEATURE_SESSION_MODEL_SWITCH.into()]
                        } else {
                            Vec::new()
                        },
                    }),
            ) {
                Ok((response, updated)) => {
                    switched = Some(response);
                    state = updated;
                }
                Err(error) => switch_error = Some(error),
            }
        }
        let observed = block_on(session.state()).unwrap();
        assert_eq!(
            serde_json::to_value(&state).unwrap(),
            serde_json::to_value(observed).unwrap()
        );
        let catalog = block_on(session.model_lister().unwrap().models(ModelsRequest {
            session_id: state.session_id.clone(),
            allow_degraded_features: vec![FEATURE_MODELS_LIST.into()],
        }));
        let closed = block_on(session.close());
        println!(
            "{}",
            serde_json::json!({
                "state": state, "initialState": initial, "nativeId": session.native_session_id(),
                "switchResponse": switched,
                "switchError": switch_error.as_ref().map(|error| error.to_string()),
                "switchDetails": switch_error.as_ref().map(switch_details),
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
