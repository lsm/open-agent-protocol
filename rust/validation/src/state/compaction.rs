use oxap_protocol::*;

use super::{payload, State};
use crate::codes;

impl State {
    pub(super) fn compaction_pair_event(&mut self, at: (usize, usize), e: &Envelope, run_id: &str) {
        let mut found = Vec::new();
        let mut wrong_stop = None;
        let run = self.runs.get_mut(run_id).unwrap();
        match e.envelope_type.as_str() {
            "run.compaction.started" => {
                let p: RunCompactionStartedPayload = payload(e);
                if run.compaction_run != (p.reason == CompactionReason::REQUESTED) {
                    found.push((
                        codes::COMPACTION_RUN_MISMATCH,
                        "/payload/reason",
                        "requested compaction must belong to a session.compact run",
                    ));
                }
                if !run.open_compaction.is_empty()
                    || run.compactions.contains_key(p.compaction_id.as_str())
                {
                    found.push((
                        codes::COMPACTION_UNPAIRED,
                        "/payload/compaction_id",
                        "a compaction overlaps or reuses an earlier identity",
                    ));
                } else {
                    run.open_compaction = p.compaction_id.as_str().to_owned();
                    run.compactions
                        .insert(run.open_compaction.clone(), CompactionOutcome::default());
                }
            }
            "run.compaction.ended" => {
                let p: RunCompactionEndedPayload = payload(e);
                if p.outcome == CompactionOutcome::FAILED && p.error.is_none() {
                    found.push((
                        codes::COMPACTION_FAILED_WITHOUT_ERROR,
                        "/payload/error",
                        "a failed compaction carries the error that failed it",
                    ));
                }
                if run.open_compaction != p.compaction_id.as_str() {
                    found.push((
                        codes::COMPACTION_ENDED_WITHOUT_START,
                        "/payload/compaction_id",
                        "compaction ended without its started event",
                    ));
                } else {
                    run.compactions
                        .insert(p.compaction_id.as_str().to_owned(), p.outcome);
                    run.open_compaction.clear();
                }
            }
            "run.completed" | "run.failed" | "run.cancelled" if !run.open_compaction.is_empty() => {
                found.push((
                    codes::COMPACTION_OPEN_AT_TERMINAL,
                    "/type",
                    "run terminated while a compaction was open",
                ));
            }
            _ => {}
        }
        if run.compaction_run && run.started {
            let terminal = matches!(
                e.envelope_type.as_str(),
                "run.completed" | "run.failed" | "run.cancelled"
            );
            if terminal && run.compactions.len() != 1 {
                found.push((
                    codes::COMPACTION_RUN_MISMATCH,
                    "/type",
                    "a compaction run carries exactly one compaction",
                ));
            } else if terminal
                && !run.compaction_continue
                && e.envelope_type == EnvelopeType::RUN_COMPLETED
                && run
                    .compactions
                    .values()
                    .all(|outcome| *outcome == CompactionOutcome::COMPLETED)
                && payload::<RunCompletedPayload>(e).stop_reason != "compacted"
            {
                wrong_stop = Some(payload::<RunCompletedPayload>(e).stop_reason);
            } else if !terminal
                && run.compactions.is_empty()
                && !matches!(
                    e.envelope_type.as_str(),
                    "run.started" | "run.compaction.started"
                )
            {
                found.push((
                    codes::COMPACTION_RUN_MISMATCH,
                    "/type",
                    "a compaction run opens with its compaction",
                ));
            }
        }
        if let Some(reason) = wrong_stop {
            self.add_expected(
                codes::COMPACTION_STOP_REASON_MISMATCH,
                at,
                e,
                "/payload/stop_reason",
                "a compaction run that did not continue settles on its compaction",
                "compacted",
                &reason,
                &[run_id],
            );
        }
        for (code, pointer, message) in found {
            self.add(code, at, e, pointer, message);
        }
    }
}
