use oap_protocol::{
    ActiveRun, EnvelopeId, MessageSubmitResponse, RunStatus, SessionState, SessionStatus,
    RELATIONSHIP_PRIMARY,
};

pub(crate) fn with_admitted_run(
    mut state: SessionState,
    admission: &MessageSubmitResponse,
    request: &EnvelopeId,
) -> SessionState {
    let Some(run) = admission.run_id.clone() else {
        return state;
    };
    let queued = admission.status == RunStatus::QUEUED;
    let mut entry = ActiveRun {
        run_id: run.clone(),
        status: admission.status.clone(),
        relationship: RELATIONSHIP_PRIMARY.into(),
        ..ActiveRun::default()
    };
    if !request.as_str().is_empty() {
        entry.admitted_submit_requests = vec![request.clone()];
    }
    if queued {
        let ahead = state
            .active_runs
            .iter()
            .filter(|existing| existing.status == RunStatus::QUEUED)
            .count();
        entry.queue_position = Some(ahead as i64 + 1);
    }
    state.active_runs.push(entry);
    if !queued {
        state.active_run_id = Some(run);
        state.status = SessionStatus::RUNNING;
    } else if state.status == SessionStatus::IDLE {
        state.status = SessionStatus::QUEUED;
    }
    state
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_queued_admission_takes_the_place_behind_the_queued_runs_and_queues_an_idle_session() {
        let state = SessionState {
            status: SessionStatus::IDLE,
            active_runs: vec![ActiveRun {
                run_id: "run-1".into(),
                status: RunStatus::QUEUED,
                ..ActiveRun::default()
            }],
            ..SessionState::default()
        };
        let admission = MessageSubmitResponse {
            run_id: Some("run-2".into()),
            status: RunStatus::QUEUED,
            ..MessageSubmitResponse::default()
        };
        let queued = with_admitted_run(state, &admission, &"".into());
        assert_eq!(queued.status, SessionStatus::QUEUED);
        assert_eq!(queued.active_run_id, None);
        assert_eq!(queued.active_runs[1].queue_position, Some(2));
        assert!(queued.active_runs[1].admitted_submit_requests.is_empty());
    }

    #[test]
    fn a_session_with_no_admitted_run_is_left_as_it_was() {
        let state = SessionState {
            status: SessionStatus::IDLE,
            ..SessionState::default()
        };
        let unchanged = with_admitted_run(state, &MessageSubmitResponse::default(), &"req".into());
        assert_eq!(unchanged.status, SessionStatus::IDLE);
        assert!(unchanged.active_runs.is_empty());
    }
}
