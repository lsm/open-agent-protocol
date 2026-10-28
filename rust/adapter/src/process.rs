use std::io::{Read, Write};
use std::process::{ChildStdin, ChildStdout, Command, ExitStatus, Stdio};
use std::sync::{Arc, LazyLock, Mutex, MutexGuard};
use std::time::Duration;

use crossbeam_channel::Receiver;
use regex::Regex;

const DEFAULT_STDERR_LIMIT: usize = 64 << 10;

#[derive(Clone, Debug, Default)]
pub struct ChildConfig {
    pub path: String,
    pub args: Vec<String>,
    pub dir: String,
    pub env: Option<Vec<String>>,
    pub stderr_limit: usize,
}

#[derive(Default)]
struct Stderr {
    bytes: Vec<u8>,
    truncated: bool,
}

#[derive(Clone)]
pub struct Child {
    child: Arc<Mutex<std::process::Child>>,
    stdin: Arc<Mutex<Option<ChildStdin>>>,
    stderr: Arc<Mutex<Stderr>>,
    stderr_done: Receiver<()>,
    exited: Receiver<()>,
    exit: Arc<Mutex<Option<crossbeam_channel::Sender<()>>>>,
    status: Arc<Mutex<Option<ExitStatus>>>,
}

fn lock<T>(mutex: &Mutex<T>) -> MutexGuard<'_, T> {
    mutex.lock().unwrap_or_else(|poison| poison.into_inner())
}

static SECRET_LINE: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"(?i)(authorization|x-api-key|api[_-]?key|auth[_-]?token|password|secret|token)(\s*[:=]\s*)(?:Bearer\s+)?([^\s,;]+)").expect("pattern")
});
static SECRET_QUOTED: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r#"(?i)((?:authorization|x-api-key|api[_-]?key|auth[_-]?token|password|secret|token)"?\s*[:=]\s*)"[^"]*""#).expect("pattern")
});

pub fn redact(value: &str) -> String {
    let quoted = SECRET_QUOTED.replace_all(value, "${1}\"[REDACTED]\"");
    SECRET_LINE
        .replace_all(&quoted, "${1}${2}[REDACTED]")
        .trim()
        .to_owned()
}

pub fn wait_error(status: Option<ExitStatus>) -> Option<String> {
    let status = status?;
    if status.success() {
        return None;
    }
    if let Some(code) = status.code() {
        return Some(format!("exit status {code}"));
    }
    #[cfg(unix)]
    {
        use std::os::unix::process::ExitStatusExt;
        if let Some(signal) = status.signal() {
            let name = match signal {
                1 => "hangup".to_owned(),
                2 => "interrupt".to_owned(),
                6 => "aborted".to_owned(),
                9 => "killed".to_owned(),
                11 => "segmentation fault".to_owned(),
                13 => "broken pipe".to_owned(),
                15 => "terminated".to_owned(),
                other => format!("signal {other}"),
            };
            return Some(format!("signal: {name}"));
        }
    }
    Some("process exited".into())
}

impl Child {
    pub fn spawn(config: &ChildConfig) -> std::io::Result<(Self, ChildStdout)> {
        let mut command = Command::new(&config.path);
        command.args(&config.args);
        if !config.dir.is_empty() {
            command.current_dir(&config.dir);
        }
        if let Some(env) = &config.env {
            command.env_clear();
            for entry in env {
                if let Some((name, value)) = entry.split_once('=') {
                    command.env(name, value);
                }
            }
        }
        command
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        let mut child = command.spawn()?;
        let stdin = child.stdin.take().expect("piped stdin");
        let stdout = child.stdout.take().expect("piped stdout");
        let mut pipe = child.stderr.take().expect("piped stderr");
        let limit = if config.stderr_limit == 0 {
            DEFAULT_STDERR_LIMIT
        } else {
            config.stderr_limit
        };
        let stderr = Arc::new(Mutex::new(Stderr::default()));
        let (stderr_sender, stderr_done) = crossbeam_channel::bounded::<()>(0);
        let captured = stderr.clone();
        std::thread::spawn(move || {
            let mut chunk = [0u8; 4096];
            while let Ok(read) = pipe.read(&mut chunk) {
                if read == 0 {
                    break;
                }
                let mut held = lock(&captured);
                let remaining = limit.saturating_sub(held.bytes.len());
                if read > remaining {
                    held.truncated = true;
                }
                let take = read.min(remaining);
                held.bytes.extend_from_slice(&chunk[..take]);
            }
            drop(stderr_sender);
        });
        let (exit, exited) = crossbeam_channel::bounded::<()>(0);
        Ok((
            Self {
                child: Arc::new(Mutex::new(child)),
                stdin: Arc::new(Mutex::new(Some(stdin))),
                stderr,
                stderr_done,
                exited,
                exit: Arc::new(Mutex::new(Some(exit))),
                status: Arc::new(Mutex::new(None)),
            },
            stdout,
        ))
    }

    pub fn writer(&self) -> impl Write + Send + 'static {
        SharedStdin(self.stdin.clone())
    }

    pub fn release_stdin(&self) {
        lock(&self.stdin).take();
    }

    pub fn watch(&self, reader_done: Receiver<()>, drain: Duration) {
        let child = self.clone();
        std::thread::spawn(move || {
            let _ = reader_done.recv();
            let _ = child.stderr_done.recv_timeout(drain);
            let waited = loop {
                if let Ok(Some(exit)) = lock(&child.child).try_wait() {
                    break Some(exit);
                }
                std::thread::sleep(Duration::from_millis(10));
            };
            *lock(&child.status) = waited;
            child.release_stdin();
            lock(&child.exit).take();
        });
    }

    pub fn exited(&self) -> Receiver<()> {
        self.exited.clone()
    }

    pub fn exit_status(&self) -> Option<ExitStatus> {
        *lock(&self.status)
    }

    pub fn stderr(&self) -> String {
        let held = lock(&self.stderr);
        let mut text = String::from_utf8_lossy(&held.bytes).into_owned();
        if held.truncated {
            text.push_str(" [truncated]");
        }
        redact(&text)
    }

    pub fn drain_stderr(&self, within: Duration) {
        let _ = self.stderr_done.recv_timeout(within);
    }

    pub fn kill(&self) {
        let _ = lock(&self.child).kill();
    }

    #[cfg(unix)]
    pub fn interrupt(&self) {
        let id = lock(&self.child).id();
        let _ = Command::new("kill")
            .args(["-INT", &id.to_string()])
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .status();
    }

    #[cfg(not(unix))]
    pub fn interrupt(&self) {
        self.kill();
    }

    pub fn await_exit(&self, within: Duration) -> bool {
        matches!(
            self.exited.recv_timeout(within),
            Err(crossbeam_channel::RecvTimeoutError::Disconnected)
        )
    }
}

struct SharedStdin(Arc<Mutex<Option<ChildStdin>>>);

impl Write for SharedStdin {
    fn write(&mut self, data: &[u8]) -> std::io::Result<usize> {
        match lock(&self.0).as_mut() {
            Some(stdin) => stdin.write(data),
            None => Err(std::io::ErrorKind::BrokenPipe.into()),
        }
    }

    fn flush(&mut self) -> std::io::Result<()> {
        match lock(&self.0).as_mut() {
            Some(stdin) => stdin.flush(),
            None => Ok(()),
        }
    }
}
