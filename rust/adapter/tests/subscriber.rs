use std::thread;
use std::time::Duration;

use futures::executor::block_on;
use futures::StreamExt;
use oap_adapter::subscriber::Subscriber;
use oap_adapter::{Error, EventStream};
use oap_protocol::{Envelope, RunId};

fn envelope(sequence: u64) -> Envelope {
    Envelope {
        run_id: Some(RunId::from("run-1")),
        sequence: Some(sequence),
        ..Envelope::default()
    }
}

fn collect(stream: EventStream) -> (Vec<u64>, Option<Error>) {
    let mut sequences = Vec::new();
    let mut failure = None;
    for item in block_on(stream.collect::<Vec<_>>()) {
        match item {
            Ok(envelope) => sequences.push(envelope.sequence.unwrap_or_default()),
            Err(error) => failure = Some(error),
        }
    }
    (sequences, failure)
}

#[test]
fn a_terminal_offer_is_delivered_and_ends_the_stream() {
    let subscriber = Subscriber::new(4);
    assert!(subscriber.offer(envelope(1), false));
    assert!(!subscriber.offer(envelope(2), true));
    let (sequences, failure) = collect(subscriber.stream());
    assert_eq!(sequences, [1, 2]);
    assert!(failure.is_none());
}

#[test]
fn an_offer_past_capacity_ends_the_stream_with_an_overflow() {
    let subscriber = Subscriber::new(2);
    assert!(subscriber.offer(envelope(1), false));
    assert!(subscriber.offer(envelope(2), false));
    assert!(!subscriber.offer(envelope(3), false));
    let (sequences, failure) = collect(subscriber.stream());
    assert_eq!(sequences, [1, 2]);
    assert!(matches!(failure, Some(Error::EventStreamOverflow)));
}

#[test]
fn a_replayed_envelope_does_not_count_against_capacity() {
    let subscriber = Subscriber::new(1);
    subscriber.replay(envelope(1));
    subscriber.replay(envelope(2));
    subscriber.replay(envelope(3));
    subscriber.close();
    let (sequences, failure) = collect(subscriber.stream());
    assert_eq!(sequences, [1, 2, 3]);
    assert!(failure.is_none());
}

#[test]
fn close_ends_the_stream_after_what_was_queued() {
    let subscriber = Subscriber::new(4);
    assert!(subscriber.offer(envelope(1), false));
    subscriber.close();
    let (sequences, failure) = collect(subscriber.stream());
    assert_eq!(sequences, [1]);
    assert!(failure.is_none());
}

#[test]
fn a_waiting_reader_wakes_when_an_envelope_arrives() {
    let subscriber = Subscriber::new(4);
    let producer = subscriber.clone();
    let writer = thread::spawn(move || {
        thread::sleep(Duration::from_millis(50));
        producer.offer(envelope(1), true);
    });
    let (sequences, failure) = collect(subscriber.stream());
    writer.join().unwrap();
    assert_eq!(sequences, [1]);
    assert!(failure.is_none());
}
