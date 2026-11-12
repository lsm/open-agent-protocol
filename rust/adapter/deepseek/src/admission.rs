use crate::native::{Event, InboxSpliced, StepBoundary, TurnEnd, TurnStart, UserMessage};

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

fn data<T: serde::de::DeserializeOwned + Default>(event: &Event) -> T {
    event.data_as().unwrap_or_default()
}

fn proves(message: &UserMessage, receipt: &str) -> bool {
    message.id == receipt && message.source.direct_user()
}

impl Candidate {
    pub fn evaluate<'a>(
        &mut self,
        receipt: &str,
        buffered: impl IntoIterator<Item = &'a Event>,
    ) -> Admission {
        if receipt.is_empty() {
            return Admission::Pending;
        }
        let (mut matches, mut turn, mut step) = (0, 0, 0);
        let mut turn_start: Option<&Event> = None;
        let mut candidate: Vec<Event> = Vec::new();
        let (mut entered, mut closed) = (false, false);
        for event in buffered {
            match event.kind.as_str() {
                "agent/inbox/spliced" => {
                    if let Ok(spliced) = event.data_as::<InboxSpliced>() {
                        matches += spliced
                            .inserted
                            .iter()
                            .filter(|message| proves(message, receipt))
                            .count();
                    }
                }
                "turn/start" => {
                    turn = data::<TurnStart>(event).turn;
                    (step, closed, entered) = (0, false, false);
                    turn_start = Some(event);
                    candidate = vec![event.clone()];
                }
                "step/start" => {
                    let boundary: StepBoundary = data(event);
                    if turn != boundary.turn {
                        continue;
                    }
                    if entered {
                        candidate.push(event.clone());
                        continue;
                    }
                    step = boundary.step;
                    candidate = vec![turn_start.cloned().unwrap_or_default(), event.clone()];
                }
                "user/message" => {
                    if turn == 0 || step == 0 {
                        continue;
                    }
                    candidate.push(event.clone());
                    if !entered && proves(&data(event), receipt) {
                        entered = true;
                        (self.turn, self.step) = (turn, step);
                        self.events = candidate.clone();
                    }
                }
                "turn/end" => {
                    if data::<TurnEnd>(event).turn == turn {
                        candidate.push(event.clone());
                        closed = true;
                        if !entered {
                            (turn, step) = (0, 0);
                            candidate.clear();
                        }
                    }
                }
                _ if turn != 0 => candidate.push(event.clone()),
                _ => {}
            }
        }
        if matches != 1 || self.turn == 0 {
            if closed || matches > 1 {
                return Admission::Refused;
            }
            return Admission::Pending;
        }
        if closed || (turn == self.turn && candidate.len() > self.events.len()) {
            self.events = candidate;
        }
        Admission::Admitted
    }
}
