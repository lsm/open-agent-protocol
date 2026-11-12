#![cfg(unix)]

use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Mutex, MutexGuard};
use std::time::Duration;

use oap_adapter::{Error, InteractionResolution, OpenRequest, ResumeRequest};
use oap_adapter_deepseek::adapter::{Adapter, Config};
use oap_adapter_deepseek::native::{SERVER_NAME, SERVER_VERSION};
use oap_adapter_deepseek::process::ProcessConfig;
use oap_adapter_deepseek::runtime::Runtime;
use oap_protocol::{
    CompactionKind, CompactionPolicy, Participant, ReasoningLevel, SessionStatus, ToolDefinition,
};
use serde_json::Value;

static SPAWNING: Mutex<()> = Mutex::new(());
static NEXT: AtomicU64 = AtomicU64::new(0);

struct Fixture {
    _serial: MutexGuard<'static, ()>,
    dir: PathBuf,
}

impl Fixture {
    fn new() -> Self {
        let serial = SPAWNING.lock().unwrap_or_else(|poison| poison.into_inner());
        let dir = std::env::temp_dir().join(format!(
            "oap-deepseek-session-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::SeqCst)
        ));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        Self {
            _serial: serial,
            dir,
        }
    }

    fn adapter(&self) -> Adapter {
        let dir = self.dir.to_string_lossy().into_owned();
        let info = format!(
            r#"{{"serverInfo":{{"name":"{SERVER_NAME}","version":"{}"}}}}"#,
            *SERVER_VERSION
        );
        let script = format!(
            r#"if [ "$1" = --patch ]; then cp "$2" "{dir}/patch"; printf '%s' "$2" > "{dir}/patch-path"; fi
read line; printf '%s\n' "$line" >> "{dir}/wire"; printf '%s\n' '{{"jsonrpc":"2.0","id":1,"result":{info}}}'
read line; printf '%s\n' "$line" >> "{dir}/wire"; printf '%s\n' '{{"jsonrpc":"2.0","id":2,"result":{{}}}}'
cat >/dev/null"#
        );
        Adapter::new(Config {
            process: ProcessConfig {
                path: "/bin/sh".into(),
                args: vec!["-c".into(), script, "sh".into()],
                dir,
                env: Some(vec!["PATH=/usr/bin:/bin".into()]),
                shutdown_timeout: Some(Duration::from_secs(2)),
                ..ProcessConfig::default()
            },
            provider: "deepseek".into(),
            model: "m".into(),
            runtime: Runtime::new(|| 42, |kind| format!("{kind}-minted")),
            ..Config::default()
        })
        .unwrap()
    }

    fn wire(&self) -> Vec<Value> {
        std::fs::read_to_string(self.dir.join("wire"))
            .unwrap_or_default()
            .lines()
            .map(|line| serde_json::from_str(line).unwrap())
            .collect()
    }
}

impl Drop for Fixture {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.dir);
    }
}

fn request(id: &str) -> OpenRequest {
    OpenRequest {
        session_id: id.into(),
        participant: Participant {
            id: "user".into(),
            ..Participant::default()
        },
        ..OpenRequest::default()
    }
}

fn refused_feature(error: Error) -> (String, &'static str) {
    match error {
        Error::UnsupportedControl(control) => (control.feature, control.reason),
        other => panic!("not a control refusal: {other:?}"),
    }
}

#[test]
fn a_reopen_or_attached_tools_are_refused_before_any_spawn() {
    let fixture = Fixture::new();
    let adapter = fixture.adapter();
    let mut reopen = request("s");
    reopen.reopen = true;
    assert_eq!(
        refused_feature(adapter.open(reopen).err().unwrap()),
        ("open.reopen".into(), "unadvertised")
    );
    let mut tools = request("s");
    tools.tools = vec![ToolDefinition {
        name: "read".into(),
        ..ToolDefinition::default()
    }];
    assert_eq!(
        refused_feature(adapter.open(tools).err().unwrap()).1,
        "unadvertised"
    );
    assert!(fixture.wire().is_empty());
}

#[test]
fn open_starts_the_runtime_with_its_settings_and_reports_an_idle_session() {
    let fixture = Fixture::new();
    let policy = CompactionPolicy {
        kind: CompactionKind::OFF,
        ..CompactionPolicy::default()
    };
    let mut open = request("s");
    open.reasoning_level = ReasoningLevel::HIGH;
    open.compaction_policy = Some(policy.clone());
    let session = fixture.adapter().open(open).unwrap();
    let wire = fixture.wire();
    assert_eq!(wire[0]["method"], "initialize");
    assert_eq!(wire[0]["params"]["reasoningEffort"], "high");
    assert_eq!(
        std::fs::read_to_string(fixture.dir.join("patch")).unwrap(),
        "- id: compaction-basic\n  disabled: true\n"
    );
    let patch = std::fs::read_to_string(fixture.dir.join("patch-path")).unwrap();
    assert!(!PathBuf::from(patch).exists());
    let state = session.state().unwrap();
    assert_eq!(state.session_id.as_str(), "s");
    assert_eq!(state.status, SessionStatus::IDLE);
    assert_eq!(state.current_model_id, "m");
    assert_eq!(state.updated_at_ms, 42);
    assert_eq!(state.reasoning_level, ReasoningLevel::HIGH);
    assert_eq!(state.compaction_policy, Some(policy));
    assert_eq!(session.native_session_id(), "s");
    session.close().unwrap();
}

#[test]
fn an_unnamed_session_takes_a_minted_id_and_starts_without_a_patch() {
    let fixture = Fixture::new();
    let session = fixture.adapter().open(request("")).unwrap();
    assert_eq!(
        session.state().unwrap().session_id.as_str(),
        "session-minted"
    );
    assert_eq!(session.native_session_id(), "session-minted");
    assert!(!fixture.dir.join("patch").exists());
    assert!(fixture.wire()[0]["params"].get("reasoningEffort").is_none());
    session.close().unwrap();
}

#[test]
fn controls_the_pinned_wire_lacks_are_refused() {
    let fixture = Fixture::new();
    let session = fixture.adapter().open(request("s")).unwrap();
    assert_eq!(
        session.resolve(InteractionResolution::default()),
        Err(Error::InteractionNotFound)
    );
    assert_eq!(
        session.cancel(&"run-1".into()).err(),
        Some(Error::Harness(
            "deepseek adapter: operation unavailable".into()
        ))
    );
    assert_eq!(
        session
            .resume(ResumeRequest {
                run_id: "run-1".into(),
                after_sequence: 0,
            })
            .err(),
        Some(Error::RunNotFound)
    );
    session.close().unwrap();
}

#[test]
fn close_shuts_the_runtime_down_once_and_retires_the_session() {
    let fixture = Fixture::new();
    let session = fixture.adapter().open(request("s")).unwrap();
    session.close().unwrap();
    session.close().unwrap();
    let methods: Vec<_> = fixture
        .wire()
        .iter()
        .map(|frame| frame["method"].as_str().unwrap().to_owned())
        .collect();
    assert_eq!(methods, ["initialize", "shutdown"]);
    assert_eq!(session.state().err(), Some(Error::SessionClosed));
    assert_eq!(
        session
            .resume(ResumeRequest {
                run_id: "run-1".into(),
                after_sequence: 0,
            })
            .err(),
        Some(Error::SessionClosed)
    );
}
