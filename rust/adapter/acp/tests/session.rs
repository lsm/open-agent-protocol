use std::path::PathBuf;
use std::process::Command;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use futures::executor::block_on;
use oap_adapter::{Adapter as AdapterContract, Error, OpenRequest};
use oap_adapter_acp::{process::ProcessConfig, Adapter, Config};
use oap_protocol::{
    ModelsRequest, Participant, SessionModelSwitchRequest, SessionStatus, FEATURE_MODELS_LIST,
    FEATURE_SESSION_MODEL_SWITCH,
};
use serde_json::{json, Value};

static NEXT_FIXTURE: AtomicU64 = AtomicU64::new(0);

struct Fixture(PathBuf);

impl Fixture {
    fn new() -> Self {
        let suffix = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        Self::with_timestamp(suffix)
    }

    fn with_timestamp(suffix: u128) -> Self {
        let unique = NEXT_FIXTURE.fetch_add(1, Ordering::Relaxed);
        let path = std::env::temp_dir().join(format!(
            "oap-acp-session-{}-{suffix}-{unique}",
            std::process::id()
        ));
        std::fs::create_dir(&path).unwrap();
        Self(path)
    }

    fn adapter(&self, result: Value) -> Adapter {
        self.adapter_with_switches(result, Vec::new())
    }

    fn adapter_with_switches(&self, result: Value, replies: Vec<Value>) -> Adapter {
        let script = r#"pidfile=$1; printf '%s' $$ > "$pidfile"; read -r request; printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{}}}'; read -r request; printf '%s\n' "$2"; shift 2; for reply in "$@"; do read -r request; printf '%s\n' "$reply"; done; if read -r waiting; then printf '%s' "$waiting" > "$pidfile.waiting"; read -r waiting; fi; exit 0"#;
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
                ]
                .into_iter()
                .chain(replies.into_iter().map(|reply| reply.to_string()))
                .collect(),
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

#[test]
fn fixture_paths_are_unique_for_equal_clock_readings() {
    let first = Fixture::with_timestamp(0);
    let second = Fixture::with_timestamp(0);
    assert_ne!(first.0, second.0);
}

fn switch_request(session: &dyn oap_adapter::Session, model: &str) -> SessionModelSwitchRequest {
    SessionModelSwitchRequest {
        session_id: block_on(session.state()).unwrap().session_id,
        model_id: model.into(),
        allow_degraded_features: vec![FEATURE_SESSION_MODEL_SWITCH.into()],
    }
}

#[test]
fn a_confirmed_model_switch_records_model_reasoning_and_catalog_default() {
    let fixture = Fixture::new();
    let mut confirmed = result("luna");
    confirmed["configOptions"]
        .as_array_mut()
        .unwrap()
        .push(json!({
            "id":"thought", "category":"thought_level", "currentValue":"adaptive",
            "options":[{"value":"adaptive", "name":"HİGH"}]
        }));
    let adapter = fixture.adapter_with_switches(
        result("sol"),
        vec![json!({"jsonrpc":"2.0", "id":3, "result":confirmed})],
    );
    let session = block_on(adapter.open(request())).unwrap();
    let (response, updated) = block_on(
        session
            .model_switcher()
            .unwrap()
            .switch_model(switch_request(&*session, "luna")),
    )
    .unwrap();
    assert_eq!(response.previous_model_id, "sol");
    assert_eq!(response.model_id, "luna");
    assert_eq!(updated.current_model_id, "luna");
    assert_eq!(updated.reasoning_level.as_str(), "high");
    assert_eq!(
        serde_json::to_value(block_on(session.state()).unwrap()).unwrap(),
        serde_json::to_value(updated).unwrap()
    );
    let listed = block_on(session.model_lister().unwrap().models(ModelsRequest {
        allow_degraded_features: vec![FEATURE_MODELS_LIST.into()],
        ..ModelsRequest::default()
    }))
    .unwrap();
    assert!(listed
        .models
        .models
        .iter()
        .any(|model| model.id == "luna" && model.default));
    block_on(session.close()).unwrap();
    fixture.assert_reaped();
}

#[test]
fn model_switch_refusals_preserve_the_previous_state() {
    for reply in [
        json!({"jsonrpc":"2.0", "id":3, "error":{"code":-32602,"message":"no"}}),
        json!({"jsonrpc":"2.0", "id":3, "result":result("sol")}),
        json!({"jsonrpc":"2.0", "id":3, "result":{"configOptions":[
            {"id":"model","currentValue":"luna"}, {"id":"model","currentValue":"luna"}
        ]}}),
    ] {
        let fixture = Fixture::new();
        let adapter = fixture.adapter_with_switches(result("sol"), vec![reply]);
        let session = block_on(adapter.open(request())).unwrap();
        let original = block_on(session.state()).unwrap();
        let error = block_on(
            session
                .model_switcher()
                .unwrap()
                .switch_model(switch_request(&*session, "luna")),
        )
        .unwrap_err();
        let Error::UnsupportedControl(control) = error else {
            panic!("{error}")
        };
        assert_eq!(control.feature, FEATURE_SESSION_MODEL_SWITCH);
        assert_eq!(control.reason, "unsatisfiable");
        assert_eq!(control.field, "model_id");
        assert_eq!(
            serde_json::to_value(block_on(session.state()).unwrap()).unwrap(),
            serde_json::to_value(original).unwrap()
        );
        block_on(session.close()).unwrap();
        fixture.assert_reaped();
    }
}

#[test]
fn a_model_switch_checks_opt_in_scope_and_the_listed_values() {
    let fixture = Fixture::new();
    let adapter = fixture.adapter(result("sol"));
    let session = block_on(adapter.open(request())).unwrap();
    let switcher = session.model_switcher().unwrap();
    let allowed = switch_request(&*session, "luna");
    assert_eq!(
        block_on(switcher.switch_model(SessionModelSwitchRequest {
            allow_degraded_features: Vec::new(),
            ..allowed.clone()
        }))
        .unwrap_err(),
        Error::DegradedControl(FEATURE_SESSION_MODEL_SWITCH.into())
    );
    assert_eq!(
        block_on(switcher.switch_model(SessionModelSwitchRequest {
            session_id: "other".into(),
            ..allowed.clone()
        }))
        .unwrap_err(),
        Error::InvalidSubmission
    );
    assert_eq!(
        block_on(switcher.switch_model(SessionModelSwitchRequest {
            model_id: "absent".into(),
            ..allowed
        }))
        .unwrap_err(),
        Error::ModelNotFound("absent".into())
    );
    block_on(session.close()).unwrap();
    fixture.assert_reaped();
}

#[test]
fn closing_a_session_releases_a_quiet_model_switch_future() {
    use std::future::Future;
    use std::task::Context;
    use std::time::Instant;
    let fixture = Fixture::new();
    let adapter = fixture.adapter(result("sol"));
    let session = block_on(adapter.open(request())).unwrap();
    let mut switching = Box::pin(
        session
            .model_switcher()
            .unwrap()
            .switch_model(switch_request(&*session, "luna")),
    );
    let waker = futures::task::noop_waker();
    assert!(switching
        .as_mut()
        .poll(&mut Context::from_waker(&waker))
        .is_pending());
    let ready = fixture.0.join("pid.waiting");
    let limit = Instant::now() + Duration::from_secs(3);
    while !ready.exists() && Instant::now() < limit {
        std::thread::sleep(Duration::from_millis(1));
    }
    assert!(ready.exists());
    block_on(session.close()).unwrap();
    assert!(block_on(switching).is_err());
    fixture.assert_reaped();
}

#[test]
fn concurrent_model_switches_commit_in_native_request_order() {
    let fixture = Fixture::new();
    let mut first = result("luna");
    first["configOptions"]
        .as_array_mut()
        .unwrap()
        .push(json!({"id":"thought", "category":"thought_level", "currentValue":"high"}));
    let adapter = fixture.adapter_with_switches(
        result("sol"),
        vec![
            json!({"jsonrpc":"2.0", "id":3, "result":first}),
            json!({"jsonrpc":"2.0", "id":4, "result":result("sol")}),
        ],
    );
    let session = block_on(adapter.open(request())).unwrap();
    let switcher = session.model_switcher().unwrap();
    let one = switch_request(&*session, "luna");
    let two = switch_request(&*session, "sol");
    let (one, two) =
        block_on(async { futures::join!(switcher.switch_model(one), switcher.switch_model(two)) });
    assert_eq!(one.unwrap().0.previous_model_id, "sol");
    assert_eq!(two.unwrap().0.previous_model_id, "luna");
    let state = block_on(session.state()).unwrap();
    assert_eq!(state.current_model_id, "sol");
    assert!(state.reasoning_level.is_empty());
    block_on(session.close()).unwrap();
    fixture.assert_reaped();
}
