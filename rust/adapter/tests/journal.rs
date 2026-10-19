use std::sync::mpsc;
use std::thread;
use std::time::Duration;

use futures::executor::block_on;
use futures::StreamExt;
use oap_adapter::journal::{Journal, FOLLOWER_BUFFER};
use oap_adapter::{Error, EventStream, ReplayGap};
use oap_protocol::*;

fn envelope(run: &str, sequence: u64) -> Envelope {
    Envelope {
        envelope_type: EnvelopeType::CONTENT_DELTA,
        run_id: Some(RunId::from(run)),
        sequence: Some(sequence),
        payload: Some(serde_json::value::to_raw_value(&serde_json::json!({"n": 1})).unwrap()),
        ..Envelope::default()
    }
}

fn append_run(journal: &Journal, run: &str, count: u64, terminal: bool) {
    for sequence in 1..=count {
        journal.append(envelope(run, sequence), terminal && sequence == count);
    }
}

fn collect(stream: EventStream) -> (Vec<Envelope>, Option<Error>) {
    let mut envelopes = Vec::new();
    let mut error = None;
    for result in block_on(stream.collect::<Vec<_>>()) {
        match result {
            Ok(envelope) => envelopes.push(envelope),
            Err(err) => error = Some(err),
        }
    }
    (envelopes, error)
}

fn assert_contiguous(envelopes: &[Envelope], count: usize) {
    assert_eq!(envelopes.len(), count);
    for (index, envelope) in envelopes.iter().enumerate() {
        assert_eq!(envelope.sequence, Some(index as u64 + 1));
    }
}

#[test]
fn a_stalled_follower_receives_every_retained_envelope_without_overflow() {
    let journal = Journal::new(1000);
    let stream = journal.follow("run", 0);
    append_run(&journal, "run", 500, true);
    let (envelopes, error) = collect(stream);
    assert_eq!(error, None);
    assert_contiguous(&envelopes, 500);
}

#[test]
fn a_follower_whose_next_envelope_was_evicted_overflows_and_resume_reports_a_gap() {
    let journal = Journal::new(16);
    let stream = journal.follow("run", 0);
    append_run(&journal, "run", 100, false);
    let (envelopes, error) = collect(stream);
    assert_eq!(error, Some(Error::EventStreamOverflow));
    assert!(
        envelopes.len() >= FOLLOWER_BUFFER && envelopes.len() < 85,
        "{}",
        envelopes.len()
    );
    assert_contiguous(&envelopes, envelopes.len());
    let cursor = envelopes.len() as u64;
    let (recovery, replay) = journal.resume(SessionState::default(), "run", cursor, 100);
    assert_eq!(
        recovery.replay_gap,
        Some(ReplayGap {
            requested_after: cursor,
            oldest_available: 85,
            latest_available: 100
        })
    );
    assert!(collect(replay).0.is_empty());
}

#[test]
fn resume_replays_the_retained_suffix_after_its_cursor() {
    let journal = Journal::new(64);
    append_run(&journal, "run", 5, true);
    let (recovery, replay) = journal.resume(SessionState::default(), "run", 2, 5);
    assert_eq!(
        (
            recovery.replayed_from,
            recovery.replayed_through,
            recovery.replay_gap
        ),
        (3, 5, None)
    );
    let sequences: Vec<_> = collect(replay)
        .0
        .iter()
        .map(|e| e.sequence.unwrap())
        .collect();
    assert_eq!(sequences, [3, 4, 5]);
    assert_eq!(journal.ended("run"), Some(5));
    assert_eq!(journal.ended("other"), None);
}

#[test]
fn delivered_envelopes_are_copies_of_the_journal() {
    let journal = Journal::new(8);
    append_run(&journal, "run", 1, true);
    let (mut first, _) = collect(journal.follow("run", 0));
    first[0].sequence = Some(99);
    let (second, _) = collect(journal.follow("run", 0));
    assert_eq!(second[0].sequence, Some(1));
}

#[test]
fn close_ends_waiting_blocked_and_unbound_followers() {
    let journal = Journal::new(1000);
    let waiting = journal.follow("idle", 0);
    let blocked = journal.follow("busy", 0);
    append_run(&journal, "busy", 200, false);
    let unbound = journal.reserve().stream();
    journal.close();
    let (delivered, error) = collect(waiting);
    assert!(delivered.is_empty() && error.is_none());
    let (delivered, error) = collect(blocked);
    assert_eq!(error, None);
    assert_eq!(delivered.len(), FOLLOWER_BUFFER);
    let (delivered, error) = collect(unbound);
    assert!(delivered.is_empty() && error.is_none());
}

#[test]
fn a_reservation_delivers_the_run_it_is_bound_to() {
    let journal = Journal::new(64);
    let mut reservation = journal.reserve();
    append_run(&journal, "other", 3, true);
    reservation.bind("run");
    append_run(&journal, "run", 5, true);
    let (envelopes, error) = collect(reservation.stream());
    assert_eq!(error, None);
    assert_contiguous(&envelopes, 5);
    assert!(envelopes
        .iter()
        .all(|e| e.run_id.as_ref().map(RunId::as_str) == Some("run")));
}

#[test]
fn a_released_reservation_ends_without_delivering() {
    let journal = Journal::new(64);
    let mut reservation = journal.reserve();
    reservation.release();
    append_run(&journal, "run", 2, true);
    let (delivered, error) = collect(reservation.stream());
    assert!(delivered.is_empty() && error.is_none());
}

#[test]
fn a_waiting_follower_wakes_when_another_thread_appends() {
    let journal = Journal::new(64);
    let stream = journal.follow("run", 0);
    let (done, received) = mpsc::channel();
    let reader = thread::spawn(move || done.send(collect(stream)).unwrap());
    thread::sleep(Duration::from_millis(20));
    append_run(&journal, "run", 3, true);
    let (envelopes, error) = received
        .recv_timeout(Duration::from_secs(2))
        .expect("the follower never woke");
    reader.join().unwrap();
    assert_eq!(error, None);
    assert_contiguous(&envelopes, 3);
}

#[test]
fn a_reservation_bound_after_close_delivers_nothing() {
    let journal = Journal::new(64);
    let mut reservation = journal.reserve();
    append_run(&journal, "run", 3, true);
    journal.close();
    reservation.bind("run");
    let (delivered, error) = collect(reservation.stream());
    assert!(
        delivered.is_empty() && error.is_none(),
        "{}",
        delivered.len()
    );
}
