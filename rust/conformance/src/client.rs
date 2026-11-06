use std::collections::VecDeque;
use std::process::{Command, ExitStatus};
use std::time::{Duration, Instant};

use oap_protocol::{Envelope, EnvelopeId};

use crate::{ControlFrame, Frame, Process, ProcessError, Transcript};

pub const DEFAULT_LINE_DEADLINE: Duration = Duration::from_secs(300);
pub const CONTROL_ANSWER_BUDGET: Duration = Duration::from_secs(10);

#[derive(Debug, thiserror::Error)]
pub enum ClientError {
    #[error(transparent)]
    Process(#[from] ProcessError),
    #[error("conformance: the endpoint produced no more lines")]
    EndpointGone,
    #[error("conformance: the endpoint answered no control frame")]
    ControlUnanswered,
    #[error("conformance: no {target} within {within:?}")]
    Timeout { target: String, within: Duration },
    #[error("conformance: this runner closed the endpoint after {0}")]
    Closed(String),
}

pub struct Client {
    process: Process,
    responses: VecDeque<Envelope>,
    events: VecDeque<Envelope>,
    controls: VecDeque<ControlFrame>,
    transcript: Transcript,
    deadline: Duration,
    dead: Option<String>,
    eof: bool,
}

impl Client {
    pub fn spawn(command: &mut Command, deadline: Duration) -> Result<Self, ClientError> {
        Ok(Self {
            process: Process::spawn(command)?,
            responses: VecDeque::new(),
            events: VecDeque::new(),
            controls: VecDeque::new(),
            transcript: Transcript::default(),
            deadline: if deadline.is_zero() {
                DEFAULT_LINE_DEADLINE
            } else {
                deadline
            },
            dead: None,
            eof: false,
        })
    }

    pub fn send(&mut self, envelope: Envelope) -> Result<(), ClientError> {
        self.transcript.push(envelope.clone());
        self.write(Frame::Envelope(Box::new(envelope)))
    }

    pub fn probe(&mut self, envelope: Envelope) -> Result<(), ClientError> {
        self.transcript.probe(envelope.id.clone());
        self.write(Frame::Envelope(Box::new(envelope)))
    }

    pub fn send_control(&mut self, frame: ControlFrame) -> Result<(), ClientError> {
        self.write(Frame::Control(frame))
    }

    fn write(&mut self, frame: Frame) -> Result<(), ClientError> {
        if let Some(reason) = &self.dead {
            return Err(ClientError::Closed(reason.clone()));
        }
        Ok(self.process.send(&frame)?)
    }

    pub fn response(&mut self, id: &EnvelopeId) -> Result<Envelope, ClientError> {
        self.wait_for(self.deadline, true, &format!("answer to {id}"), |client| {
            let index = client
                .responses
                .iter()
                .position(|candidate| candidate.in_reply_to.as_ref() == Some(id))?;
            client.responses.remove(index)
        })
    }

    pub fn event(&mut self) -> Result<Envelope, ClientError> {
        self.wait_for(self.deadline, true, "event", |client| {
            client.events.pop_front()
        })
    }

    pub fn control(&mut self, id: &str) -> Result<ControlFrame, ClientError> {
        self.wait_for(
            self.deadline.min(CONTROL_ANSWER_BUDGET),
            false,
            &format!("control answer to {id}"),
            |client| {
                let index = client.controls.iter().position(|frame| frame.id == id)?;
                client.controls.remove(index)
            },
        )
    }

    pub fn close_input(&mut self) {
        self.process.close_input();
    }

    pub fn wait(&mut self) -> Result<ExitStatus, ClientError> {
        self.wait_for(self.deadline, true, "endpoint EOF", |client| {
            client.eof.then_some(())
        })?;
        Ok(self
            .process
            .wait_exit(self.deadline.min(Duration::from_secs(30)))?)
    }

    pub fn close(&mut self) -> Result<(), ClientError> {
        self.process.close().map_err(ProcessError::from)?;
        Ok(())
    }

    pub fn closed_by_runner(&self) -> Option<&str> {
        self.dead.as_deref()
    }

    pub fn transcript(&self) -> Vec<Envelope> {
        self.transcript.envelopes()
    }

    fn wait_for<T>(
        &mut self,
        within: Duration,
        fatal: bool,
        target: &str,
        mut take: impl FnMut(&mut Self) -> Option<T>,
    ) -> Result<T, ClientError> {
        let start = Instant::now();
        loop {
            if let Some(answer) = take(self) {
                return Ok(answer);
            }
            if self.eof {
                return Err(ClientError::EndpointGone);
            }
            if let Some(reason) = &self.dead {
                return Err(ClientError::Closed(reason.clone()));
            }
            let remaining = within.saturating_sub(start.elapsed());
            if remaining.is_zero() {
                for _ in 0..self.process.buffered() {
                    self.pull(Duration::ZERO)?;
                }
                if let Some(answer) = take(self) {
                    return Ok(answer);
                }
                return Err(self.expire(within, fatal, target));
            }
            match self.pull(remaining) {
                Err(ClientError::Process(ProcessError::Timeout)) => {
                    return Err(self.expire(within, fatal, target));
                }
                result => result?,
            }
        }
    }

    fn pull(&mut self, within: Duration) -> Result<(), ClientError> {
        match self.process.next(within)? {
            None => self.eof = true,
            Some(Frame::Control(frame)) => self.controls.push_back(frame),
            Some(Frame::Envelope(envelope)) => {
                let envelope = *envelope;
                self.transcript.push(envelope.clone());
                if envelope
                    .in_reply_to
                    .as_ref()
                    .is_some_and(|id| !id.as_str().is_empty())
                {
                    self.responses.push_back(envelope);
                } else {
                    self.events.push_back(envelope);
                }
            }
        }
        Ok(())
    }

    fn expire(&mut self, within: Duration, fatal: bool, target: &str) -> ClientError {
        if !fatal {
            return ClientError::ControlUnanswered;
        }
        let error = ClientError::Timeout {
            target: target.to_owned(),
            within,
        };
        self.dead = Some(error.to_string());
        let _ = self.process.close();
        error
    }
}
