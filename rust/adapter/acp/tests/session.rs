use std::path::PathBuf;
use std::process::Command;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use futures::executor::block_on;
use oap_adapter::{Adapter as AdapterContract, Error, OpenRequest};
use oap_adapter_acp::{process::ProcessConfig, Adapter, Config};
use oap_protocol::{ModelsRequest, Participant, SessionStatus, FEATURE_MODELS_LIST};
use serde_json::{json, Value};

struct Fixture(PathBuf);

impl Fixture {
    fn new() -> Self {
        let suffix = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let path =
            std::env::temp_dir().join(format!("oap-acp-session-{}-{suffix}", std::process::id()));
        std::fs::create_dir(&path).unwrap();
        Self(path)
    }

    fn adapter(&self, result: Value) -> Adapter {
        let script = r#"printf '%s' $$ > "$1"; read -r request; printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{}}}'; read -r request; printf '%s\n' "$2"; read -r waiting; exit 0"#;
        Adapter::new(Config {
            process: ProcessConfig {
                path: "/bin/sh".into(),
                dir: self.0.to_string_lossy().into(),
                args: vec![
                    "-c".into(),
                    script.into(),
                    "fixture".into(),
                    self.0.join("pid").to_string_lossy().into(),
                    json!({"jsonrpc":"2.0", "id":2, "result":result}).to_string(),
                ],
                env: Some(Vec::new()),
                shutdown_timeout: Some(Duration::from_millis(300)),
                ..ProcessConfig::default()
            },
            ..Config::default()
        })
        .unwrap()
    }

    fn assert_reaped(&self) {
        let pid = std::fs::read_to_string(self.0.join("pid")).unwrap();
        assert!(
            !Command::new("/bin/kill")
                .args(["-0", &pid])
                .output()
                .unwrap()
                .status
                .success(),
            "child {pid} remains"
        );
    }
}

impl Drop for Fixture {
    fn drop(&mut self) {
        std::fs::remove_dir_all(&self.0).unwrap();
    }
}

fn request() -> OpenRequest {
    OpenRequest {
        participant: Participant {
            id: "tester".into(),
            ..Participant::default()
        },
        ..OpenRequest::default()
    }
}

fn result(current: &str) -> Value {
    json!({"sessionId":"native-session", "configOptions":[{
        "id":"model", "name":"Model", "type":"select", "category":"model", "currentValue":current,
        "options":[{"value":"sol", "name":"Sol"}, {"value":"luna", "name":"Luna"}]
    }]})
}

#[test]
fn a_new_session_reports_idle_native_identity_and_an_opted_in_model_catalog() {
    let fixture = Fixture::new();
    let adapter = fixture.adapter(result("luna"));
    let session = block_on(adapter.open(request())).unwrap();
    let state = block_on(session.state()).unwrap();
    assert_eq!(state.session_id.as_str(), "session-1");
    assert_eq!(state.status, SessionStatus::IDLE);
    assert_eq!(state.current_model_id, "luna");
    assert!(state.updated_at_ms > 0);
    assert_eq!(
        session.native_session_id().as_deref(),
        Some("native-session")
    );
    let models = session.model_lister().unwrap();
    assert_eq!(
        block_on(models.models(ModelsRequest::default())).unwrap_err(),
        Error::DegradedControl(FEATURE_MODELS_LIST.into())
    );
    let listed = block_on(models.models(ModelsRequest {
        session_id: state.session_id.clone(),
        allow_degraded_features: vec![FEATURE_MODELS_LIST.into()],
    }))
    .unwrap();
    assert_eq!(listed.revision, adapter.describe().capability_revision);
    assert_eq!(listed.models.current_model_id, "luna");
    assert_eq!(
        listed
            .models
            .models
            .iter()
            .map(|model| (model.id.as_str(), model.default))
            .collect::<Vec<_>>(),
        [("sol", false), ("luna", true)]
    );
    assert_eq!(
        block_on(models.models(ModelsRequest {
            session_id: "other".into(),
            allow_degraded_features: vec![FEATURE_MODELS_LIST.into()],
        }))
        .unwrap_err(),
        Error::RunNotFound
    );
    block_on(async {
        let (one, two) = futures::join!(session.close(), session.close());
        one.unwrap();
        two.unwrap();
    });
    assert_eq!(block_on(session.state()).unwrap_err(), Error::SessionClosed);
    assert_eq!(block_on(session.close()), Ok(()));
    fixture.assert_reaped();
}

#[test]
fn invalid_session_new_replies_close_and_reap_the_child() {
    for result in [Value::Null, json!({}), json!({"sessionId":""})] {
        let fixture = Fixture::new();
        let adapter = fixture.adapter(result);
        assert!(block_on(adapter.open(request())).is_err());
        fixture.assert_reaped();
    }
}

#[test]
fn catalog_placeholders_do_not_become_current_model_ids() {
    for current in ["", "default", "current"] {
        let fixture = Fixture::new();
        let session = block_on(fixture.adapter(result(current)).open(request())).unwrap();
        assert_eq!(block_on(session.state()).unwrap().current_model_id, "");
        block_on(session.close()).unwrap();
        fixture.assert_reaped();
    }
}

#[test]
fn a_missing_native_model_option_is_a_typed_catalog_refusal() {
    let fixture = Fixture::new();
    let adapter = fixture.adapter(json!({"sessionId":"native"}));
    let session = block_on(adapter.open(OpenRequest {
        session_id: "provided".into(),
        ..request()
    }))
    .unwrap();
    assert_eq!(
        block_on(session.state()).unwrap().session_id.as_str(),
        "provided"
    );
    let error = block_on(session.model_lister().unwrap().models(ModelsRequest {
        allow_degraded_features: vec![FEATURE_MODELS_LIST.into()],
        ..ModelsRequest::default()
    }))
    .unwrap_err();
    let Error::UnsupportedControl(control) = error else {
        panic!("{error}")
    };
    assert_eq!(control.feature, FEATURE_MODELS_LIST);
    assert_eq!(control.reason, "unsatisfiable");
    block_on(session.close()).unwrap();
    fixture.assert_reaped();
}
