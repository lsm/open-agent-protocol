use std::path::PathBuf;
use std::process::Command;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{SystemTime, UNIX_EPOCH};

use serde_json::{json, Value};

#[path = "support/opening_config.rs"]
mod support;

static NEXT: AtomicU64 = AtomicU64::new(0);
struct Fixture(PathBuf);
impl Drop for Fixture {
    fn drop(&mut self) {
        std::fs::remove_dir_all(&self.0).unwrap();
    }
}

fn options() -> Value {
    json!([{"id":"mode","category":"mode","currentValue":"code","options":[{"value":"ask","name":"Ask"},{"value":"code","name":"Code"}]}, {"id":"model","category":"model","currentValue":"sol","options":[{"value":"sol","name":"Sol"},{"value":"luna","name":"Luna"}]}, {"id":"effort","category":"thought_level","currentValue":"medium","options":[{"value":"medium","name":"Medium"},{"value":"deep","name":"HIGH"}]}])
}

fn run(mut native: Value, mut input: Value) -> Value {
    let serial = NEXT.fetch_add(1, Ordering::Relaxed);
    let suffix = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    let fixture = Fixture(std::env::temp_dir().join(format!(
        "oap-acp-opening-{}-{suffix}-{serial}",
        std::process::id()
    )));
    std::fs::create_dir(&fixture.0).unwrap();
    if native.get("options").is_none() {
        native["options"] = options();
    }
    std::fs::write(fixture.0.join("native.json"), native.to_string()).unwrap();
    let python = Command::new("python3")
        .args(["-c", "import sys; print(sys.executable)"])
        .output()
        .unwrap();
    assert!(python.status.success());
    input["python"] = json!(String::from_utf8(python.stdout).unwrap().trim());
    input["agent"] = json!(PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../../.claude/skills/verify/scripts/fixtures/acp-opening-config-agent.py"));
    input["case"] = json!(fixture.0);
    support::run(&input)
}

#[test]
fn opening_reasoning_is_confirmed_after_real_native_drain_for_new_and_reloaded_sessions() {
    for reopen in [false, true] {
        let out = run(json!({}), json!({"reasoning":"high","reopen":reopen}));
        assert_eq!(out["state"]["reasoning_level"], "high");
        assert_eq!(out["state"]["current_model_id"], "sol");
        assert_eq!(out["native_id"], "native");
        assert_eq!(out["state"]["status"], "idle");
    }
}

#[test]
fn a_reasoning_request_is_refused_when_the_agent_cannot_offer_or_confirm_it() {
    let mut unconfirmed = options();
    unconfirmed[2]["currentValue"] = json!("medium");
    for native in [
        json!({"options":[]}),
        json!({"replies":[{"configOptions":unconfirmed}]}),
        json!({"error":{"code":-32602,"message":"refused"}}),
    ] {
        let out = run(native, json!({"reasoning":"high"}));
        assert_eq!(out["failure"]["feature"], "session.reasoning");
        assert_eq!(out["failure"]["reason"], "unsatisfiable");
        assert_eq!(out["failure"]["field"], "reasoning_level");
    }
}

#[test]
fn a_confirmed_reasoning_request_stays_explicit_when_the_returned_snapshot_omits_its_category() {
    let out = run(
        json!({"replies":[{"configOptions":[{"id":"effort","currentValue":"deep"}]}]}),
        json!({"reasoning":"high"}),
    );
    assert_eq!(out["state"]["reasoning_level"], "high");
}

#[test]
fn dropping_a_quiet_opening_setting_future_reaps_the_native_agent() {
    let out = run(
        json!({"quiet":true}),
        json!({"reasoning":"high","cancel":true}),
    );
    assert_eq!(out, json!({"cancelled":true}));
}
