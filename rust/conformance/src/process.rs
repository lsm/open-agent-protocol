use std::io::{self, BufReader, Read};
use std::os::unix::net::UnixStream;
use std::process::{Child, ChildStdin, ChildStdout, Command, ExitStatus, Stdio};
use std::thread::{self, JoinHandle};
use std::time::{Duration, Instant};

use crossbeam_channel::{bounded, Receiver, RecvTimeoutError, Sender};
use rustix::event::{poll, PollFd, PollFlags};

use crate::{read_frame, write_frame, Frame, WireError};

#[derive(Debug, thiserror::Error)]
pub enum ProcessError {
    #[error("conformance: {0}")]
    Io(#[from] io::Error),
    #[error(transparent)]
    Wire(#[from] WireError),
    #[error("conformance: the endpoint produced no line within the wait budget")]
    Timeout,
    #[error("conformance: the endpoint had not exited {0:?} after stdout closed")]
    ExitTimeout(Duration),
}

pub struct Process {
    child: Child,
    input: Option<ChildStdin>,
    frames: Receiver<Result<Frame, WireError>>,
    stop: Option<Sender<()>>,
    interrupt: Option<UnixStream>,
    reader: Option<JoinHandle<()>>,
}

impl Process {
    pub fn spawn(command: &mut Command) -> Result<Self, ProcessError> {
        let (interrupt, cancellation) = UnixStream::pair()?;
        let (stop, stopped) = bounded(0);
        let (sender, frames) = bounded(64);
        let mut child = command
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .spawn()?;
        let input = child.stdin.take();
        let stdout = child.stdout.take().expect("piped stdout");
        let reader = match thread::Builder::new()
            .spawn(move || read_frames(stdout, cancellation, stopped, sender))
        {
            Ok(reader) => reader,
            Err(error) => {
                let _ = child.kill();
                let _ = child.wait();
                return Err(error.into());
            }
        };
        Ok(Self {
            child,
            input,
            frames,
            stop: Some(stop),
            interrupt: Some(interrupt),
            reader: Some(reader),
        })
    }

    pub fn send(&mut self, frame: &Frame) -> Result<(), ProcessError> {
        let input = self
            .input
            .as_mut()
            .ok_or_else(|| io::Error::from(io::ErrorKind::BrokenPipe))?;
        write_frame(input, frame)?;
        Ok(())
    }

    pub fn next(&self, within: Duration) -> Result<Option<Frame>, ProcessError> {
        match self.frames.recv_timeout(within) {
            Ok(frame) => Ok(Some(frame?)),
            Err(RecvTimeoutError::Disconnected) => Ok(None),
            Err(RecvTimeoutError::Timeout) => Err(ProcessError::Timeout),
        }
    }

    pub(crate) fn buffered(&self) -> usize {
        self.frames.len()
    }

    pub fn close_input(&mut self) {
        self.input.take();
    }

    pub fn wait_exit(&mut self, within: Duration) -> Result<ExitStatus, ProcessError> {
        let start = Instant::now();
        loop {
            if let Some(status) = self.child.try_wait()? {
                return Ok(status);
            }
            if start.elapsed() >= within {
                self.close()?;
                return Err(ProcessError::ExitTimeout(within));
            }
            thread::sleep(Duration::from_millis(10).min(within.saturating_sub(start.elapsed())));
        }
    }

    pub fn close(&mut self) -> io::Result<()> {
        self.stop.take();
        self.interrupt.take();
        self.close_input();
        let _ = self.child.kill();
        let waited = self.child.wait();
        let joined = self.reader.take().map(|reader| reader.join());
        waited?;
        if joined.is_some_and(|result| result.is_err()) {
            return Err(io::Error::other("conformance: stdout reader panicked"));
        }
        Ok(())
    }
}

impl Drop for Process {
    fn drop(&mut self) {
        let _ = self.close();
    }
}

struct EndpointOutput {
    stdout: ChildStdout,
    cancellation: UnixStream,
}

impl Read for EndpointOutput {
    fn read(&mut self, buffer: &mut [u8]) -> io::Result<usize> {
        let mut descriptors = [
            PollFd::new(&self.stdout, PollFlags::IN),
            PollFd::new(&self.cancellation, PollFlags::IN),
        ];
        poll(&mut descriptors, None)?;
        if !descriptors[1].revents().is_empty() {
            return Ok(0);
        }
        self.stdout.read(buffer)
    }
}

fn read_frames(
    stdout: ChildStdout,
    cancellation: UnixStream,
    stopped: Receiver<()>,
    sender: Sender<Result<Frame, WireError>>,
) {
    let mut input = BufReader::new(EndpointOutput {
        stdout,
        cancellation,
    });
    loop {
        let frame = match read_frame(&mut input) {
            Ok(Some(frame)) => Ok(frame),
            Ok(None) => break,
            Err(error) => Err(error),
        };
        let fatal = matches!(&frame, Err(WireError::Io(_)));
        crossbeam_channel::select! {
            send(sender, frame) -> sent => if sent.is_err() { break; },
            recv(stopped) -> _ => break,
        }
        if fatal {
            break;
        }
    }
}
