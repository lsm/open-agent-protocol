use std::path::PathBuf;
use std::process::Command;
use std::sync::atomic::{AtomicU64, Ordering};

use serde_json::{json, Value};

#[path = "support/live_config.rs"]
mod support;

static NEXT: AtomicU64 = AtomicU64::new(0);

struct Fixture(PathBuf);

impl Fixture {
    fn run(active: bool, steps: Value) -> Value {
        let fixture = Self(std::env::temp_dir().join(format!(
            "oap-acp-config-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        )));
        std::fs::create_dir(&fixture.0).unwrap();
        std::fs::write(
            fixture.0.join("opened.json"),
            json!([model("old", "sol")]).to_string(),
        )
        .unwrap();
        let python = Command::new("python3")
            .args(["-c", "import sys; print(sys.executable)"])
            .output()
            .unwrap();
        assert!(python.status.success());
        let agent = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .join("../../../.claude/skills/verify/scripts/fixtures/acp-config-agent.py");
        support::run(
            &json!({"case":fixture.0,"python":String::from_utf8(python.stdout).unwrap().trim(),"agent":agent,"active":active,"steps":steps}),
        )
    }
}

impl Drop for Fixture {
    fn drop(&mut self) {
        std::fs::remove_dir_all(&self.0).unwrap();
    }
}

fn model(id: &str, current: &str) -> Value {
    json!({"id":id,"category":"model","currentValue":current,"options":[{"value":"luna","name":"Luna"},{"group":"g","options":[{"value":"adaptive","name":"Adaptive"}]}]})
}

fn update(options: Value) -> Value {
    json!({"update":{"sessionUpdate":"config_option_update","configOptions":options}})
}

#[test]
fn an_idle_native_update_replaces_state_catalog_and_the_next_switch_option() {
    let mut changed = update(
        json!([model("new-picker", "luna"), {"id":"effort","category":"thought_level","currentValue":"deep","options":[{"value":"deep","name":"HİGH"}]}]),
    );
    changed["switch"] = json!("adaptive");
    let out = Fixture::run(false, json!([changed]));
    let snapshot = &out["snapshots"][0];
    assert_eq!(snapshot["state"]["current_model_id"], "luna");
    assert_eq!(snapshot["state"]["reasoning_level"], "high");
    assert!(
        snapshot["state"]["updated_at_ms"].as_i64().unwrap()
            > out["initial"]["updated_at_ms"].as_i64().unwrap()
    );
    assert_eq!(
        snapshot["catalog"]["ok"]["models"],
        json!([{"id":"luna","display_name":"Luna","default":true},{"id":"adaptive","display_name":"Adaptive"}])
    );
    assert_eq!(snapshot["switch"]["ok"]["previous_model_id"], "luna");
    assert_eq!(out["final"]["current_model_id"], "adaptive");
}

#[test]
fn removing_model_and_reasoning_options_refuses_a_catalog_and_switch_without_losing_the_model() {
    let mut removed = update(json!([]));
    removed["switch"] = json!("adaptive");
    let out = Fixture::run(
        false,
        json!([
            update(
                json!([model("new", "luna"), {"category":"thought_level","currentValue":"HIGH"}])
            ),
            removed
        ]),
    );
    let snapshot = &out["snapshots"][1];
    assert_eq!(snapshot["state"]["current_model_id"], "luna");
    assert!(snapshot["state"].get("reasoning_level").is_none());
    assert_eq!(
        snapshot["catalog"]["error"],
        json!({"feature":"models.list","reason":"unsatisfiable"})
    );
    assert_eq!(
        snapshot["switch"]["error"],
        json!({"feature":"session.model.switch","reason":"unsatisfiable"})
    );
}

#[test]
fn config_updates_during_a_run_change_state_without_inventing_stream_events() {
    let out = Fixture::run(
        true,
        json!([update(
            json!([model("new", "luna"), {"category":"thought_level","currentValue":"low"}])
        )]),
    );
    let state = &out["snapshots"][0]["state"];
    assert_eq!(state["current_model_id"], "luna");
    assert_eq!(state["reasoning_level"], "low");
    assert_eq!(state["status"], "running");
    assert_eq!(
        out["events"]
            .as_array()
            .unwrap()
            .iter()
            .map(|event| event["type"].as_str().unwrap())
            .collect::<Vec<_>>(),
        ["run.started", "content.delta", "run.completed"]
    );
    assert_eq!(out["final"]["current_model_id"], "luna");
}

#[test]
fn placeholders_preserve_the_model_and_unchanged_settings_preserve_the_timestamp() {
    let out = Fixture::run(
        false,
        json!([
            update(json!([model("new", "current")])),
            update(json!([model("other", "default")])),
            update(json!([model("last", "")]))
        ]),
    );
    for snapshot in out["snapshots"].as_array().unwrap() {
        assert_eq!(snapshot["state"]["current_model_id"], "sol");
        assert_eq!(
            snapshot["state"]["updated_at_ms"],
            out["initial"]["updated_at_ms"]
        );
        assert_eq!(
            snapshot["catalog"]["ok"]["models"]
                .as_array()
                .unwrap()
                .len(),
            2
        );
    }
}

#[test]
fn wrong_session_and_malformed_idle_updates_do_not_change_the_recorded_settings() {
    let mut wrong = update(json!([model("wrong", "luna")]));
    wrong["sessionId"] = json!("another");
    let out = Fixture::run(
        false,
        json!([wrong, update(json!([{"id":"bad","currentValue":3}]))]),
    );
    for snapshot in out["snapshots"].as_array().unwrap() {
        assert_eq!(snapshot["state"], out["initial"]);
    }
}
