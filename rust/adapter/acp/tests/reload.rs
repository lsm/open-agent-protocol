use std::path::PathBuf;
use std::process::Command;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{SystemTime, UNIX_EPOCH};

use serde_json::{json, Value};

#[path = "support/reload.rs"]
mod support;

static NEXT: AtomicU64 = AtomicU64::new(0);

struct Fixture(PathBuf);
impl Drop for Fixture {
    fn drop(&mut self) {
        std::fs::remove_dir_all(&self.0).unwrap();
    }
}

fn options() -> Value {
    json!([{"id":"picker","category":"model","currentValue":"restored","options":[{"value":"restored","name":"Restored"}]}, {"id":"effort","category":"thought_level","currentValue":"deep","options":[{"value":"deep","name":"High"}]}])
}

fn run(mut native: Value, mut input: Value) -> Value {
    let serial = NEXT.fetch_add(1, Ordering::Relaxed);
    let suffix = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    let path = std::env::temp_dir().join(format!(
        "oap-acp-reload-{}-{suffix}-{serial}",
        std::process::id()
    ));
    let fixture = Fixture(path);
    std::fs::create_dir(&fixture.0).unwrap();
    if native.get("result").is_none() {
        native["result"] = json!({"configOptions":options()});
    }
    std::fs::write(fixture.0.join("native.json"), native.to_string()).unwrap();
    let python = Command::new("python3")
        .args(["-c", "import sys; print(sys.executable)"])
        .output()
        .unwrap();
    assert!(python.status.success());
    input["python"] = json!(String::from_utf8(python.stdout).unwrap().trim());
    input["agent"] = json!(PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../../.claude/skills/verify/scripts/fixtures/acp-reload-agent.py"));
    input["case"] = json!(fixture.0);
    support::run(&input)
}

#[test]
fn load_or_resume_restores_the_binding_and_settings_without_replaying_history() {
    for capabilities in [
        json!({"loadSession":true,"sessionCapabilities":{"resume":{}}}),
        json!({"sessionCapabilities":{"resume":{}}}),
    ] {
        let out = run(json!({"capabilities":capabilities}), json!({"submit":true}));
        assert_eq!(out["native_id"], "bound-native");
        assert_eq!(out["state"]["current_model_id"], "restored");
        assert_eq!(out["state"]["reasoning_level"], "high");
        assert_eq!(out["state"]["recovery"]["recovered"], true);
        assert!(out["state"].get("transcript_cursor").is_none());
        let events = out["events"].as_array().unwrap();
        assert_eq!(
            events
                .iter()
                .map(|event| event["type"].as_str().unwrap())
                .collect::<Vec<_>>(),
            ["run.started", "content.delta", "run.completed"]
        );
        assert_eq!(events[0]["sequence"], 1);
        assert_eq!(
            events[2]["payload"]["final_response"]["content"],
            "continued"
        );
        assert!(events[2]["payload"].get("usage").is_none());
        assert_eq!(
            events[2]["extensions"]["com.agentclientprotocol.turn_usage"]["total_tokens"],
            10
        );
    }
}

#[test]
fn unadvertised_or_invalid_bindings_are_unsatisfiable_and_reap_started_peers() {
    for capabilities in [
        json!({}),
        json!({"loadSession":"true"}),
        json!({"sessionCapabilities":{"resume":true}}),
        json!({"sessionCapabilities":{"resume":null}}),
    ] {
        let out = run(json!({"capabilities":capabilities}), json!({}));
        assert_eq!(out["failure"]["feature"], "session.open.reopen");
        assert_eq!(out["failure"]["reason"], "unsatisfiable");
    }
    let out = run(
        json!({"capabilities":{"loadSession":true}}),
        json!({"native_id":""}),
    );
    assert_eq!(out["failure"]["reason"], "unsatisfiable");
}

#[test]
fn reload_rejects_malformed_results_changed_ids_and_native_errors() {
    for result in [
        Value::Null,
        json!([]),
        json!({"sessionId":"other"}),
        json!({"sessionId":""}),
        json!({"configOptions":[{"currentValue":7}]}),
    ] {
        let out = run(
            json!({"capabilities":{"loadSession":true},"result":result}),
            json!({}),
        );
        assert_eq!(out["failure"]["reason"], "unsatisfiable");
    }
    let out = run(
        json!({"capabilities":{"loadSession":true},"error":{"code":-32602,"message":"unknown session"}}),
        json!({}),
    );
    assert_eq!(out["failure"]["reason"], "unsatisfiable");
}

#[test]
fn dropping_a_quiet_reload_future_reaps_the_native_child() {
    let out = run(
        json!({"capabilities":{"loadSession":true},"quiet":true}),
        json!({"cancel":true}),
    );
    assert_eq!(out, json!({"cancelled":true}));
}
