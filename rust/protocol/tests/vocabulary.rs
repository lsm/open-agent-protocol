use oap_protocol::{Admission, EnvelopeType, RequestedDeliveryMode, RunStatus};
use serde_json::json;

#[test]
fn vocabulary_recognition_preserves_unknown_wire_values() {
    for (name, known) in [
        ("run.started", true),
        ("session.task.updated", false),
        ("com.example.future.event", false),
        ("", false),
    ] {
        let value: EnvelopeType = serde_json::from_value(json!(name)).unwrap();
        assert_eq!(value.is_known(), known);
        assert_eq!(serde_json::to_value(value).unwrap(), json!(name));
    }
    assert!(RunStatus::RUNNING.is_known());
    assert!(!RunStatus::from("paused").is_known());
    assert!(!RunStatus::default().is_known());
    assert!(Admission::STARTED.is_known());
    assert!(!Admission::from("merged").is_known());
    assert!(RequestedDeliveryMode::AUTO.is_known());
    assert!(!RequestedDeliveryMode::from("later").is_known());
}
