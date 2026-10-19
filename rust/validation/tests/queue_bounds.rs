use oap_validation::{codes, Validator};
use serde_json::{json, Value};

fn envelope(mut value: Value) -> Value {
    value["protocol"] = json!("open-agent-protocol");
    value["version"] = json!("0.1");
    value["profile"] = json!("open-agent-protocol.agent-control-core");
    value
}

fn submit(id: &str, run: &str, admission: &str, revision: &str) -> [Value; 2] {
    let (effective, status) = if admission == "queued" {
        ("queue", "queued")
    } else {
        ("start", "running")
    };
    [
        envelope(
            json!({"type": "session.message.submit.request", "id": format!("{id}q"), "session_id": "s",
            "capability_revision": revision,
            "payload": {"session_id": "s", "messages": [{"role": "user", "content": "go"}], "delivery": "auto"}}),
        ),
        envelope(
            json!({"type": "session.message.submit.response", "id": format!("{id}a"), "in_reply_to": format!("{id}q"),
            "session_id": "s", "capability_revision": revision,
            "payload": {"session_id": "s", "accepted": true, "submission_id": id, "requested_delivery": "auto",
                "effective_delivery": effective, "admission": admission, "run_id": run, "status": status,
                "delivery_resolution": "session_busy"}}),
        ),
    ]
}

fn codes_after(withdrawn: bool) -> Vec<String> {
    let mut events = vec![
        envelope(json!({"type": "capabilities.request", "id": "c1", "payload": {}})),
        envelope(
            json!({"type": "capabilities.response", "id": "c2", "in_reply_to": "c1", "capability_revision": "v1",
            "payload": {"endpoint": {"id": "agent"},
                "features": {"session.message.delivery.queue": {"level": "native"}},
                "limits": {"max_active_runs_per_session": 1, "max_queued_runs_per_session": 1}}}),
        ),
    ];
    events.extend(submit("m1", "r1", "started", "v1"));
    let revision = if withdrawn {
        events.push(envelope(
            json!({"type": "capabilities.updated", "id": "u1", "capability_revision": "v2",
            "sequence": 1, "payload": {"previous_revision": "v1"}}),
        ));
        "v2"
    } else {
        "v1"
    };
    events.extend(submit("m2", "r2", "queued", revision));
    let trace = events
        .iter()
        .map(Value::to_string)
        .collect::<Vec<_>>()
        .join("\n");
    let result = Validator::new()
        .unwrap()
        .validate(trace.as_bytes(), "queue-bounds");
    result.diagnostics.into_iter().map(|d| d.code).collect()
}

#[test]
fn a_withdrawn_descriptor_s_bounds_no_longer_judge_an_admission() {
    assert!(codes_after(false).contains(&codes::QUEUE_LIMIT_EXCEEDED.to_owned()));
    let withdrawn = codes_after(true);
    assert!(!withdrawn.contains(&codes::QUEUE_LIMIT_EXCEEDED.to_owned()));
    assert!(withdrawn.contains(&codes::ILLEGAL_RUN_TRANSITION.to_owned()));
}

fn codes_after_recovering(reservation_status: &str) -> Vec<String> {
    let mut reservation =
        json!({"run_id": "r2", "status": reservation_status, "relationship": "primary"});
    if reservation_status == "queued" {
        reservation["queue_position"] = json!(1);
    }
    let mut events = vec![
        envelope(json!({"type": "capabilities.request", "id": "c1", "payload": {}})),
        envelope(
            json!({"type": "capabilities.response", "id": "c2", "in_reply_to": "c1", "capability_revision": "v1",
            "payload": {"endpoint": {"id": "agent"},
                "features": {"session.message.delivery.queue": {"level": "native"}},
                "limits": {"max_active_runs_per_session": 3, "max_queued_runs_per_session": 1}}}),
        ),
        envelope(
            json!({"type": "session.open.request", "id": "o1", "session_id": "s", "capability_revision": "v1",
            "payload": {"session_id": "s", "recovery": {"previous_run_id": "r1", "recovered": true, "resume_cursor": "5"}}}),
        ),
        envelope(
            json!({"type": "session.open.response", "id": "o2", "in_reply_to": "o1", "session_id": "s",
            "capability_revision": "v1",
            "payload": {"session_id": "s", "status": "running", "active_run_id": "r1",
                "recovery": {"previous_run_id": "r1", "recovered": true, "resume_cursor": "5"},
                "active_runs": [
                    {"run_id": "r1", "status": "running", "relationship": "primary", "as_of_sequence": 5},
                    reservation
                ]}}),
        ),
    ];
    let mut queued = submit("m1", "r3", "queued", "v1");
    queued[0]["payload"]["delivery"] = json!("queue");
    queued[1]["payload"]["requested_delivery"] = json!("queue");
    events.extend(queued);
    let trace = events
        .iter()
        .map(Value::to_string)
        .collect::<Vec<_>>()
        .join("\n");
    let result = Validator::new()
        .unwrap()
        .validate(trace.as_bytes(), "queue-bounds");
    result.diagnostics.into_iter().map(|d| d.code).collect()
}

#[test]
fn a_recovered_reservation_counts_against_the_queued_bound() {
    assert!(codes_after_recovering("queued").contains(&codes::QUEUE_LIMIT_EXCEEDED.to_owned()));
    assert!(!codes_after_recovering("running").contains(&codes::QUEUE_LIMIT_EXCEEDED.to_owned()));
}
