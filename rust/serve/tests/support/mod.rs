use std::io::{BufReader, Read, Write};
use std::sync::mpsc::{channel, Receiver};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use oap_serve::stdio::{serve, StdioOptions};
use oap_serve::Hub;
use serde_json::Value;

#[derive(Clone, Default)]
pub struct Captured(Arc<Mutex<Vec<u8>>>);

impl Captured {
    pub fn bytes(&self) -> Vec<u8> {
        self.0.lock().unwrap().clone()
    }
}

impl Write for Captured {
    fn write(&mut self, data: &[u8]) -> std::io::Result<usize> {
        self.0.lock().unwrap().extend_from_slice(data);
        Ok(data.len())
    }

    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

struct Fed {
    feed: Receiver<Vec<u8>>,
    held: Vec<u8>,
    at: usize,
}

impl Read for Fed {
    fn read(&mut self, buf: &mut [u8]) -> std::io::Result<usize> {
        if self.at == self.held.len() {
            match self.feed.recv() {
                Ok(next) => {
                    self.held = next;
                    self.at = 0;
                }
                Err(_) => return Ok(0),
            }
        }
        let n = buf.len().min(self.held.len() - self.at);
        buf[..n].copy_from_slice(&self.held[self.at..self.at + n]);
        self.at += n;
        Ok(n)
    }
}

impl Captured {
    pub fn values(&self) -> Vec<Value> {
        String::from_utf8(self.bytes())
            .unwrap()
            .lines()
            .map(|line| serde_json::from_str(line).unwrap())
            .collect()
    }
}

#[allow(dead_code)]
pub fn in_turn<L: AsRef<str>>(hub: &Hub, lines: &[L], options: &StdioOptions) -> Vec<Value> {
    let (feed, fed) = channel();
    let output = Captured::default();
    let (served_hub, served_output, served_options) =
        (hub.clone(), output.clone(), options.clone());
    let served = std::thread::spawn(move || {
        let input = BufReader::new(Fed {
            feed: fed,
            held: Vec::new(),
            at: 0,
        });
        serve(&served_hub, input, served_output, &served_options)
    });
    for line in lines {
        let line = line.as_ref();
        let id = serde_json::from_str::<Value>(line)
            .ok()
            .and_then(|value| value.get("id").cloned());
        if feed.send(format!("{line}\n").into_bytes()).is_err() {
            break;
        }
        let Some(id) = id else { continue };
        let deadline = Instant::now() + Duration::from_secs(10);
        while !served.is_finished()
            && Instant::now() < deadline
            && !output
                .values()
                .iter()
                .any(|answer| answer["id"] == id && answer.get("ok").is_some())
        {
            std::thread::sleep(Duration::from_millis(1));
        }
    }
    drop(feed);
    served.join().unwrap().unwrap();
    output.values()
}
