use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

#[derive(Clone)]
pub struct Runtime {
    clock: Arc<dyn Fn() -> i64 + Send + Sync>,
    ids: Arc<dyn Fn(&str) -> String + Send + Sync>,
}

impl std::fmt::Debug for Runtime {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter.debug_struct("Runtime").finish_non_exhaustive()
    }
}

impl Runtime {
    pub fn new(
        clock: impl Fn() -> i64 + Send + Sync + 'static,
        ids: impl Fn(&str) -> String + Send + Sync + 'static,
    ) -> Self {
        Self {
            clock: Arc::new(clock),
            ids: Arc::new(ids),
        }
    }

    pub(crate) fn now_ms(&self) -> i64 {
        (self.clock)()
    }

    pub(crate) fn new_id(&self, kind: &str) -> String {
        (self.ids)(kind)
    }
}

impl Default for Runtime {
    fn default() -> Self {
        let next = AtomicU64::new(0);
        Self::new(
            || {
                SystemTime::now()
                    .duration_since(UNIX_EPOCH)
                    .map_or(0, |time| time.as_millis() as i64)
            },
            move |kind| format!("{kind}-{}", next.fetch_add(1, Ordering::SeqCst) + 1),
        )
    }
}
