#![cfg(feature = "testkit")]

mod support;

use oap_adapter::testkit::{self, RunTrace, DEFAULT_TIMEOUT};
use support::*;

fn cancelled_run() -> (MessageSubmitResponse, Vec<Envelope>) {
    let session = session(64);
    let (admission, mut stream) = started(&session);
    let mut events = vec![testkit::next(&mut stream, DEFAULT_TIMEOUT)];
    events.extend(support::drain(&mut stream));
    cancel(&session, admission.run_id.as_ref().unwrap()).unwrap();
    events.extend(testkit::drain(&mut stream, DEFAULT_TIMEOUT));
    (admission, events)
}

fn traced(admission: MessageSubmitResponse, events: Vec<Envelope>) -> RunTrace {
    RunTrace {
        submitted: Some(plain()),
        opened: Some(session_open()),
        ..RunTrace::new(admission, descriptor(), events)
    }
}

#[test]
fn drain_returns_every_event_up_to_the_end_of_the_stream() {
    let (_, events) = cancelled_run();
    assert_eq!(
        events.last().map(|event| &event.envelope_type),
        Some(&EnvelopeType::RUN_CANCELLED)
    );
    testkit::assert_types(
        &events[events.len() - 2..],
        &[
            EnvelopeType::ACTION_CALL_CANCELLED,
            EnvelopeType::RUN_CANCELLED,
        ],
    );
}

#[test]
fn a_cancelled_run_validates_with_its_cancel_exchange_spliced_in() {
    let (admission, events) = cancelled_run();
    let mut trace = traced(admission, events);
    trace.cancelled = true;
    trace.assert_valid();
}

#[test]
fn a_cancelled_run_without_its_cancel_exchange_is_an_illegal_transition() {
    let (admission, events) = cancelled_run();
    traced(admission, events).assert_invalid("illegal_run_transition");
}

#[test]
fn the_cancel_exchange_lands_before_the_cancelling_status() {
    let (admission, events) = cancelled_run();
    let cut = testkit::cancel_cut(&events);
    let status: RunStatusUpdatedPayload = events[cut].decode_payload().unwrap();
    assert_eq!(status.status, RunStatus::CANCELLING);
    let mut trace = traced(admission, events);
    trace.cancelled = true;
    let encoded: Vec<Envelope> = serde_json::from_slice(&trace.encode().unwrap()).unwrap();
    let request = encoded
        .iter()
        .position(|event| event.envelope_type == EnvelopeType::RUN_CANCEL_REQUEST)
        .unwrap();
    assert_eq!(
        encoded[request + 2].envelope_type,
        EnvelopeType::RUN_STATUS_UPDATED
    );
}

#[test]
fn a_synthesized_submit_is_refused_when_the_descriptor_offers_submit_controls() {
    let (admission, events) = cancelled_run();
    let trace = RunTrace::new(admission, descriptor(), events);
    let error = trace.encode().unwrap_err();
    assert!(error.contains("offers submit controls"), "{error}");
}

#[test]
#[should_panic(expected = "sequence")]
fn run_events_must_carry_a_contiguous_sequence() {
    let (admission, mut events) = cancelled_run();
    events.remove(1);
    testkit::assert_run_events(&admission, CAPABILITY_REVISION, &events);
}

#[test]
#[should_panic(expected = "terminal events")]
fn a_run_without_a_terminal_is_refused() {
    let (admission, mut events) = cancelled_run();
    events.pop();
    testkit::assert_run_events(&admission, CAPABILITY_REVISION, &events);
}

#[test]
#[should_panic(expected = "capability revision")]
fn a_run_event_must_cite_the_revision() {
    let (admission, mut events) = cancelled_run();
    events[0].capability_revision = Some("other".into());
    testkit::assert_run_events(&admission, CAPABILITY_REVISION, &events);
}
