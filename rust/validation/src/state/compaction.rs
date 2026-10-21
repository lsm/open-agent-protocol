use oap_protocol::*;

use super::{payload, State};
use crate::codes;

impl State {
    pub(super) fn compaction_pair_event(&mut self, at: (usize, usize), e: &Envelope, run_id: &str) {
        let mut found = Vec::new();
        let run = self.runs.get_mut(run_id).unwrap();
        match e.envelope_type.as_str() {
            "run.compaction.started" => {
                let p: RunCompactionStartedPayload = payload(e);
                if !run.open_compaction.is_empty() {
                    found.push((
                        codes::COMPACTION_UNPAIRED,
                        "/payload/compaction_id",
                        "a compaction started before the previous one ended",
                    ));
                } else {
                    run.open_compaction = p.compaction_id.as_str().to_owned();
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
        for (code, pointer, message) in found {
            self.add(code, at, e, pointer, message);
        }
    }
}
