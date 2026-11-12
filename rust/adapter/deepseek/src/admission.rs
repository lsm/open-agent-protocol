use crate::native::Event;

#[derive(Clone, Debug, Default)]
pub struct Candidate {
    pub turn: i64,
    pub step: i64,
    pub events: Vec<Event>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Admission {
    Pending,
    Refused,
    Admitted,
}

impl Candidate {
    pub fn observe(&mut self, _receipt: &str, _event: &Event) {}

    pub fn evaluate<'a>(
        &mut self,
        _receipt: &str,
        _buffered: impl IntoIterator<Item = &'a Event>,
    ) -> Admission {
        Admission::Pending
    }
}
