use oap_protocol::{Envelope, EnvelopeId, EnvelopeType, RunId};

use crate::{ClientError, ControlFrame};

use super::{Check, Runner};

impl Runner<'_> {
    pub fn replay_run(&mut self, run: &RunId) {
        const NAME: &str = "a cursor replay is accepted and re-delivers the run";
        let original = signature(self.client.transcript(), run);
        let frame = ControlFrame {
            control: "replay".into(),
            id: self.next("replay").to_string(),
            session_id: self.session.clone(),
            run_id: run.clone(),
            after: Some(0),
            ..ControlFrame::default()
        };
        let id = frame.id.clone();
        let result = self
            .client
            .send_control(frame)
            .and_then(|()| self.client.control(&id))
            .map_err(|error| match error {
                ClientError::ControlUnanswered => {
                    "the endpoint answered nothing. An unimplemented control must still be answered with unsupported_control".into()
                }
                error => error.to_string(),
            });
        let Some(answer) = self.result(NAME, result) else {
            return;
        };
        if answer.control == "replay.gap" {
            self.checks.push(Check::failed(
                NAME,
                format!(
                    "the endpoint retains nothing at {}. Its window is {}..{}",
                    answer.requested_after, answer.oldest_available, answer.latest_available
                ),
            ));
            return;
        }
        if answer.control != "replay.accepted" {
            self.checks.push(if answer.code == "unsupported_control" {
                Check::skipped(
                    NAME,
                    "the endpoint does not implement the replay control, which the binding permits",
                )
            } else {
                Check::failed(NAME, format!("{}: {}", answer.code, answer.message))
            });
            return;
        }
        if original.is_empty() {
            self.checks.push(Check::failed(NAME, "this runner recorded no events for the run it asked to replay, so it cannot confirm re-delivery"));
            return;
        }
        let mut index = 0;
        loop {
            let result = self
                .client
                .replay_event(run)
                .map_err(|error| error.to_string());
            let Some(event) = self.result(NAME, result) else {
                return;
            };
            let got = RunEvent {
                id: event.id,
                kind: event.envelope_type,
                sequence: event
                    .sequence
                    .expect("replay_event selected a sequenced envelope"),
            };
            if original.get(index) != Some(&got) {
                self.checks.push(Check::failed(
                    NAME,
                    format!(
                        "event {index} of the replay is {got:?}. Expected {:?}",
                        original.get(index)
                    ),
                ));
                return;
            }
            index += 1;
            if terminal(&got.kind) {
                self.checks.push(Check::passed(NAME));
                return;
            }
        }
    }
}

#[derive(Debug, PartialEq, Eq)]
struct RunEvent {
    id: EnvelopeId,
    kind: EnvelopeType,
    sequence: u64,
}

fn terminal(kind: &EnvelopeType) -> bool {
    [
        EnvelopeType::RUN_COMPLETED,
        EnvelopeType::RUN_FAILED,
        EnvelopeType::RUN_CANCELLED,
    ]
    .contains(kind)
}

fn signature(envelopes: Vec<Envelope>, run: &RunId) -> Vec<RunEvent> {
    let mut signature = Vec::new();
    for envelope in envelopes {
        if envelope.run_id.as_ref() != Some(run) {
            continue;
        }
        if let Some(sequence) = envelope.sequence {
            let ended = terminal(&envelope.envelope_type);
            signature.push(RunEvent {
                id: envelope.id,
                kind: envelope.envelope_type,
                sequence,
            });
            if ended {
                break;
            }
        }
    }
    signature
}
