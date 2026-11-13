use crate::native::{ThreadResumeParams, ThreadResumeResponse};

pub fn confirm(_sent: &ThreadResumeParams, _answered: &ThreadResumeResponse) -> Result<(), String> {
    Ok(())
}
