use oap_protocol::{RunId, RunStatus, SessionId};

pub const CONTROL_UNADVERTISED: &str = "unadvertised";
pub const CONTROL_UNSATISFIABLE: &str = "unsatisfiable";

pub const STEER_NO_ACTIVE_RUN: &str = "no_active_run";
pub const STEER_TERMINAL: &str = "terminal";
pub const STEER_QUEUED: &str = "queued";
pub const STEER_UNKNOWN_TARGET: &str = "unknown_target";
pub const STEER_NOT_STEERABLE: &str = "not_steerable";
pub const STEER_CROSS_SESSION: &str = "cross_session";

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct UnsupportedControl {
    pub feature: String,
    pub reason: &'static str,
    pub tool: String,
    pub field: String,
    pub source: String,
    pub detail: String,
}

impl UnsupportedControl {
    pub fn unadvertised(feature: &str) -> Self {
        Self {
            feature: feature.to_owned(),
            reason: CONTROL_UNADVERTISED,
            ..Self::default()
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ReplayGap {
    pub requested_after: u64,
    pub oldest_available: u64,
    pub latest_available: u64,
}

#[derive(Clone, Debug, PartialEq, Eq, thiserror::Error)]
pub enum Error {
    #[error("adapter: session closed")]
    SessionClosed,
    #[error("adapter: participant identity is required")]
    InvalidParticipant,
    #[error("adapter: unsupported input")]
    UnsupportedInput,
    #[error("adapter: unsupported input: {}", control_text(.0))]
    UnsupportedControl(Box<UnsupportedControl>),
    #[error("adapter: unsupported input: {0} is degraded and was not opted into")]
    DegradedControl(String),
    #[error("adapter: a run is already active")]
    RunActive,
    #[error("adapter: invalid submission")]
    InvalidSubmission,
    #[error("adapter: run not found")]
    RunNotFound,
    #[error("adapter: replay cursor is newer than the run")]
    ReplayCursorFuture,
    #[error("adapter: run already completed or failed")]
    RunAlreadyTerminal {
        run_id: Option<RunId>,
        status: Option<RunStatus>,
    },
    #[error("adapter: interaction not found")]
    InteractionNotFound,
    #[error("adapter: interaction already resolved")]
    InteractionResolved,
    #[error("adapter: interaction resolved by undeclared participant")]
    WrongResponder,
    #[error("adapter: invalid interaction resolution")]
    InvalidResolution,
    #[error("adapter: event stream consumer fell behind; resume from the last sequence")]
    EventStreamOverflow,
    #[error("adapter: steer target cannot take guidance: run {:?} is {reason}", .run_id.as_str())]
    InvalidSteerTarget {
        run_id: RunId,
        reason: &'static str,
        target_sequence: Option<u64>,
    },
    #[error("adapter: a cursor this endpoint did not mint, or one naming another session")]
    InvalidCursor,
    #[error("adapter: model is not in the effective catalog: {0:?}")]
    ModelNotFound(String),
    #[error("no session {:?}", .0.as_str())]
    UnknownSession(SessionId),
    #[error("adapter: requested replay cursor is no longer retained")]
    ReplayGap(ReplayGap),
    #[error("adapter: no portable tool catalog is served")]
    ToolCatalogUnavailable,
}

fn control_text(control: &UnsupportedControl) -> String {
    if control.detail.is_empty() {
        format!("{} ({})", control.feature, control.reason)
    } else {
        format!(
            "{} ({}): {}",
            control.feature, control.reason, control.detail
        )
    }
}

impl Error {
    pub fn is_unsupported_input(&self) -> bool {
        matches!(
            self,
            Error::UnsupportedInput | Error::UnsupportedControl(_) | Error::DegradedControl(_)
        )
    }
}
