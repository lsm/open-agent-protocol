use oap_protocol::*;

use super::pipe::Pipe;
use super::session::Inner;
use crate::{Error, EventStream, Recovery, ReplayGap, ResumeRequest};

impl Inner {
    pub(super) fn resume(
        &mut self,
        request: ResumeRequest,
    ) -> Result<(Recovery, EventStream), Error> {
        if self.closed {
            return Err(Error::SessionClosed);
        }
        let id = request.run_id.as_str().to_owned();
        let run = self.runs.get(&id).ok_or(Error::RunNotFound)?;
        let latest = run.next_sequence - 1;
        let after = request.after_sequence;
        if after > latest {
            return Err(Error::ReplayCursorFuture);
        }
        let ours: Vec<&Envelope> = self
            .journal
            .iter()
            .filter(|e| e.run_id.as_ref().is_some_and(|r| r.as_str() == id))
            .collect();
        let oldest = ours.iter().find_map(|e| e.sequence).unwrap_or(0);
        let suffix: Vec<Envelope> = ours
            .into_iter()
            .filter(|e| e.sequence.is_some_and(|s| s > after))
            .cloned()
            .collect();
        if after < latest && (oldest == 0 || after + 1 < oldest) {
            return Err(Error::ReplayGap(ReplayGap {
                requested_after: after,
                oldest_available: oldest,
                latest_available: latest,
            }));
        }
        let mut recovery = Recovery {
            state: self.state.clone(),
            run_id: request.run_id.clone(),
            requested_after: after,
            replayed_from: after,
            replayed_through: after,
            replay_gap: None,
        };
        if let (Some(first), Some(last)) = (suffix.first(), suffix.last()) {
            recovery.replayed_from = first.sequence.unwrap_or(after);
            recovery.replayed_through = last.sequence.unwrap_or(after);
        }
        let pipe = Pipe::default();
        for envelope in suffix {
            pipe.send(Ok(envelope));
        }
        let run = self.run_mut(&id);
        if run.terminal {
            pipe.close();
        } else {
            run.subscribers.push(pipe.clone());
        }
        Ok((recovery, pipe.stream()))
    }
}
