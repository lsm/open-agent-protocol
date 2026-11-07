use oap_protocol::{Admission, RequestedDeliveryMode, RunStatus};

pub(crate) fn foreign_status(value: &RunStatus) -> bool {
    !value.is_empty() && !value.is_known()
}

pub(crate) fn foreign_admission(value: &Admission) -> bool {
    !value.is_empty() && !value.is_known()
}

pub(crate) fn foreign_delivery(value: &RequestedDeliveryMode) -> bool {
    !value.is_empty() && !value.is_known()
}
