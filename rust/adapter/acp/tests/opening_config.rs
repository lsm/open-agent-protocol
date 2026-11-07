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

#[test]
fn configured_options_are_applied_in_order_before_reasoning_and_returned_state() {
    let out = run(
        json!({}),
        json!({"wanted":{"model":"luna","mode":"ask"},"reasoning":"high"}),
    );
    assert_eq!(out["state"]["current_model_id"], "luna");
    assert_eq!(out["state"]["reasoning_level"], "high");
    assert_eq!(
        out["settings"],
        json!([
            {"sessionId":"native","configId":"mode","value":"ask"},
            {"sessionId":"native","configId":"model","value":"luna"},
            {"sessionId":"native","configId":"effort","value":"deep"}
        ])
    );
}

#[test]
fn later_configuration_and_reasoning_cannot_undo_required_values() {
    let mut mode = options();
    mode[0]["currentValue"] = json!("ask");
    let mut model = options();
    model[1]["currentValue"] = json!("luna");
    let out = run(
        json!({"replies":[{"configOptions":mode.clone()},{"configOptions":model}]}),
        json!({"wanted":{"mode":"ask","model":"luna"}}),
    );
    assert_eq!(out["failure"]["kind"], "config");
    assert!(out["failure"]["error"]
        .as_str()
        .unwrap()
        .contains("later setting changed"));
    let mut thought = options();
    thought[2]["currentValue"] = json!("deep");
    let out = run(
        json!({"replies":[{"configOptions":mode},{"configOptions":thought}]}),
        json!({"wanted":{"mode":"ask"},"reasoning":"high"}),
    );
    assert_eq!(out["failure"]["kind"], "config");
}

#[test]
fn invalid_configured_ids_values_and_advertisements_close_without_returning_a_session() {
    for wanted in [
        json!({"missing":"x"}),
        json!({"model":"unknown"}),
        json!({"":"ask"}),
        json!({"model":""}),
    ] {
        let out = run(json!({}), json!({"wanted":wanted}));
        assert_eq!(out["failure"]["kind"], "config");
    }
    let mut wrong = options();
    wrong[0]["type"] = json!("boolean");
    let out = run(json!({"options":wrong}), json!({"wanted":{"mode":"ask"}}));
    assert_eq!(out["failure"]["kind"], "config");
}
