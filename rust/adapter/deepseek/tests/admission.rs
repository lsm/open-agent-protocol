use oap_adapter_deepseek::admission::{Admission, Candidate};
use oap_adapter_deepseek::native::{decode_notification, Event, Notification};

fn event(seq: i64, kind: &str, data: &str) -> Event {
    let params = format!(
        r#"{{"sessionId":"s","event":{{"type":"{kind}","seq":{seq},"time":1,"data":{data}}}}}"#
    );
    match decode_notification("session.event", params.as_bytes()) {
        Ok(Notification::SessionEvent(notification)) => notification.event,
        other => panic!("{kind} {data} is not a valid session.event: {other:?}"),
    }
}

fn spliced(seq: i64, id: &str) -> Event {
    event(
        seq,
        "agent/inbox/spliced",
        &format!(
            r#"{{"target":"next-turn","start":0,"inserted":[{{"id":"{id}","role":"user","content":[],"source":{{"kind":"user"}}}}]}}"#
        ),
    )
}

fn turn_start(seq: i64, turn: i64) -> Event {
    event(seq, "turn/start", &format!(r#"{{"turn":{turn}}}"#))
}

fn step(seq: i64, kind: &str, turn: i64, step: i64) -> Event {
    event(seq, kind, &format!(r#"{{"turn":{turn},"step":{step}}}"#))
}

fn user(seq: i64, id: &str, source: &str) -> Event {
    event(
        seq,
        "user/message",
        &format!(r#"{{"id":"{id}","role":"user","content":[],"source":{{"kind":"{source}"}}}}"#),
    )
}

fn turn_end(seq: i64, turn: i64, reason: &str) -> Event {
    event(
        seq,
        "turn/end",
        &format!(r#"{{"turn":{turn},"reason":{{"kind":"{reason}"}}}}"#),
    )
}

fn assistant(seq: i64, turn: i64, step: i64) -> Event {
    event(
        seq,
        "assistant/message",
        &format!(
            r#"{{"turn":{turn},"step":{step},"message":{{"id":"a","role":"assistant","content":[{{"type":"text","text":"hi"}}],"source":{{"kind":"model","provider":"p","model":"m"}}}},"stream":[]}}"#
        ),
    )
}

struct Live {
    candidate: Candidate,
    buffered: Vec<Event>,
    receipt: String,
}

impl Live {
    fn new(receipt: &str) -> Self {
        Self {
            candidate: Candidate::default(),
            buffered: Vec::new(),
            receipt: receipt.into(),
        }
    }

    fn feed(&mut self, event: Event) -> Admission {
        self.buffered.push(event);
        if self.receipt.is_empty() {
            return Admission::Pending;
        }
        self.candidate.evaluate(&self.receipt, &self.buffered)
    }

    fn kinds(&self) -> Vec<String> {
        self.candidate
            .events
            .iter()
            .map(|event| event.kind.clone())
            .collect()
    }
}

#[test]
fn a_spliced_receipt_entering_a_step_as_the_user_admits_the_run() {
    let mut live = Live::new("receipt");
    assert_eq!(live.feed(spliced(1, "receipt")), Admission::Pending);
    assert_eq!(live.feed(turn_start(2, 1)), Admission::Pending);
    assert_eq!(live.feed(step(3, "step/start", 1, 1)), Admission::Pending);
    assert_eq!(live.feed(user(4, "receipt", "user")), Admission::Admitted);
    assert_eq!((live.candidate.turn, live.candidate.step), (1, 1));
    assert_eq!(live.kinds(), ["turn/start", "step/start", "user/message"]);
}

#[test]
fn a_synthetic_user_message_is_no_proof_and_its_turn_ending_refuses() {
    let mut live = Live::new("receipt");
    live.feed(spliced(1, "receipt"));
    live.feed(turn_start(2, 1));
    live.feed(step(3, "step/start", 1, 1));
    assert_eq!(
        live.feed(user(4, "receipt", "runtime-context")),
        Admission::Pending
    );
    assert_eq!(live.feed(step(5, "step/end", 1, 1)), Admission::Pending);
    assert_eq!(live.feed(turn_end(6, 1, "blocked")), Admission::Refused);
}

#[test]
fn a_receipt_never_spliced_into_the_inbox_is_not_admitted() {
    let mut live = Live::new("receipt");
    live.feed(turn_start(1, 1));
    live.feed(step(2, "step/start", 1, 1));
    assert_eq!(live.feed(user(3, "receipt", "user")), Admission::Pending);
    assert_eq!(live.feed(turn_end(4, 1, "completed")), Admission::Refused);
}

#[test]
fn a_receipt_spliced_twice_refuses_at_once() {
    let mut live = Live::new("receipt");
    assert_eq!(live.feed(spliced(1, "receipt")), Admission::Pending);
    assert_eq!(live.feed(spliced(2, "receipt")), Admission::Refused);
}

#[test]
fn another_message_turn_that_closes_first_refuses() {
    let mut live = Live::new("receipt");
    live.feed(spliced(1, "receipt"));
    live.feed(turn_start(2, 1));
    live.feed(step(3, "step/start", 1, 1));
    assert_eq!(live.feed(user(4, "other", "user")), Admission::Pending);
    assert_eq!(live.feed(turn_end(5, 1, "completed")), Admission::Refused);
}

#[test]
fn a_receipt_learned_after_the_turn_ended_admits_the_whole_turn() {
    let mut live = Live::new("");
    for event in [
        spliced(1, "receipt"),
        turn_start(2, 1),
        step(3, "step/start", 1, 1),
        user(4, "receipt", "user"),
        step(5, "step/end", 1, 1),
        step(6, "step/start", 1, 2),
        assistant(7, 1, 2),
        step(8, "step/end", 1, 2),
        turn_end(9, 1, "completed"),
    ] {
        live.feed(event);
    }
    assert_eq!(
        live.candidate.evaluate("receipt", &live.buffered),
        Admission::Admitted
    );
    assert_eq!((live.candidate.turn, live.candidate.step), (1, 1));
    assert_eq!(
        live.kinds(),
        [
            "turn/start",
            "step/start",
            "user/message",
            "step/end",
            "step/start",
            "assistant/message",
            "step/end",
            "turn/end"
        ]
    );
}

#[test]
fn a_receipt_admitted_in_a_later_turn_keeps_that_turns_events() {
    let mut live = Live::new("receipt");
    live.feed(spliced(1, "receipt"));
    live.feed(turn_start(2, 1));
    live.feed(step(3, "step/start", 1, 1));
    live.feed(user(4, "other", "user"));
    live.feed(turn_start(5, 2));
    live.feed(step(6, "step/start", 2, 1));
    assert_eq!(live.feed(user(7, "receipt", "user")), Admission::Admitted);
    assert_eq!((live.candidate.turn, live.candidate.step), (2, 1));
    let seqs: Vec<_> = live
        .candidate
        .events
        .iter()
        .map(|event| event.seq)
        .collect();
    assert_eq!(seqs, [5, 6, 7]);
}

#[test]
fn a_turn_end_of_another_turn_before_the_splice_is_not_kept_for_replay() {
    let mut live = Live::new("receipt");
    live.feed(turn_start(1, 1));
    live.feed(step(2, "step/start", 1, 1));
    assert_eq!(live.feed(user(3, "receipt", "user")), Admission::Pending);
    assert_eq!(live.feed(turn_end(4, 2, "completed")), Admission::Pending);
    assert_eq!(live.feed(spliced(5, "receipt")), Admission::Admitted);
    let seqs: Vec<_> = live
        .candidate
        .events
        .iter()
        .map(|event| event.seq)
        .collect();
    assert_eq!(seqs, [1, 2, 3]);
}
