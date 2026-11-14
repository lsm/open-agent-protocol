use oxap_protocol::Usage;

use crate::native::ThreadTokenUsageUpdatedNotification;
use crate::session::{Inner, Shared};

fn growth(now: i64, before: i64) -> Option<u64> {
    Some(u64::try_from(now.saturating_sub(before)).unwrap_or(0))
}

impl Shared {
    pub(crate) fn usage(&self, inner: &mut Inner, value: &ThreadTokenUsageUpdatedNotification) {
        if value.thread_id != self.thread_id {
            return;
        }
        let total = value.token_usage.total;
        inner.thread_usage = total;
        let Some(id) = inner.turns.get(&value.turn_id).cloned() else {
            return;
        };
        let Some(run) = inner.runs.get_mut(&id) else {
            return;
        };
        run.usage = Some(Usage {
            input_tokens: growth(total.input_tokens, run.base.input_tokens),
            output_tokens: growth(total.output_tokens, run.base.output_tokens),
            total_tokens: growth(total.total_tokens, run.base.total_tokens),
            ..Usage::default()
        });
    }
}
