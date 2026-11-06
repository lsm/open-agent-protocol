use std::collections::{HashMap, HashSet};

use oap_protocol::{Envelope, EnvelopeId, MessageSubmitResponse, RunId, SessionState};

#[derive(Default)]
pub struct Transcript {
    recorded: Vec<Envelope>,
    probes: HashSet<EnvelopeId>,
}

impl Transcript {
    pub fn probe(&mut self, id: EnvelopeId) {
        self.probes.insert(id);
    }

    pub fn push(&mut self, envelope: Envelope) {
        if !envelope
            .in_reply_to
            .as_ref()
            .is_some_and(|id| self.probes.contains(id))
        {
            self.recorded.push(envelope);
        }
    }

    pub fn envelopes(&self) -> Vec<Envelope> {
        let mut trace = Vec::new();
        let mut seen = HashSet::new();
        let mut admitted = HashSet::new();
        let mut held: HashMap<RunId, Vec<Envelope>> = HashMap::new();
        let mut order = Vec::new();
        for envelope in &self.recorded {
            if !envelope.id.as_str().is_empty() && !seen.insert(&envelope.id) {
                continue;
            }
            if let Some(run) = envelope
                .run_id
                .as_ref()
                .filter(|id| !id.as_str().is_empty())
            {
                if envelope.sequence.is_some() && !admitted.contains(run) {
                    if !held.contains_key(run) {
                        order.push(run.clone());
                    }
                    held.entry(run.clone()).or_default().push(envelope.clone());
                    continue;
                }
            }
            trace.push(envelope.clone());
            if let Some(run) = admitted_run(envelope) {
                if admitted.insert(run.clone()) {
                    trace.extend(held.remove(&run).unwrap_or_default());
                }
            }
        }
        for run in order {
            trace.extend(held.remove(&run).unwrap_or_default());
        }
        trace
    }
}

fn admitted_run(envelope: &Envelope) -> Option<RunId> {
    let run = match envelope.envelope_type.as_str() {
        "session.message.submit.response" => {
            envelope
                .decode_payload::<MessageSubmitResponse>()
                .ok()?
                .run_id
        }
        "session.open.response" => {
            envelope
                .decode_payload::<SessionState>()
                .ok()?
                .active_run_id
        }
        _ => None,
    };
    run.filter(|id| !id.as_str().is_empty())
}
