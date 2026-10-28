use std::fs::{File, OpenOptions};
use std::io::{Read, Seek, SeekFrom, Write};
use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt};
use std::path::{Path, PathBuf};
use std::sync::{Mutex, MutexGuard, PoisonError};

use crate::{sessions, Entry, Error, Store};

#[derive(Debug, Default)]
struct Cache {
    validated: u64,
    confirmed: bool,
    last_start: u64,
}

#[derive(Debug)]
pub struct FileStore {
    path: PathBuf,
    cache: Mutex<Cache>,
}

impl FileStore {
    pub fn open(path: &Path) -> Result<Self, Error> {
        if path.as_os_str().is_empty() {
            return Err(Error::Torn("a file store needs a path".into()));
        }
        if let Some(dir) = path.parent().filter(|dir| !dir.as_os_str().is_empty()) {
            std::fs::DirBuilder::new()
                .recursive(true)
                .mode(0o700)
                .create(dir)?;
        }
        OpenOptions::new()
            .create(true)
            .append(true)
            .mode(0o600)
            .open(path)?;
        let store = Self {
            path: path.to_owned(),
            cache: Mutex::default(),
        };
        store.repair(&mut store.cache())?;
        Ok(store)
    }

    fn cache(&self) -> MutexGuard<'_, Cache> {
        self.cache.lock().unwrap_or_else(PoisonError::into_inner)
    }

    fn repair(&self, cache: &mut Cache) -> Result<(), Error> {
        let size = std::fs::metadata(&self.path)?.len();
        if size == 0 {
            *cache = Cache::default();
            return Ok(());
        }
        if cache.confirmed && size == cache.validated {
            return Ok(());
        }
        let mut file = OpenOptions::new().read(true).write(true).open(&self.path)?;
        let mut from = 0;
        if cache.confirmed && size >= cache.validated && cache.last_start < size {
            let line = read_line_at(&mut file, cache.last_start, size)?;
            if line.ends_with(b"\n") && decode(&line).is_ok() {
                from = cache.validated;
            }
        }
        let tail = read_from(&mut file, from, size)?;
        let mut keep = from;
        let mut start = 0;
        for (index, byte) in tail.iter().enumerate() {
            if *byte != b'\n' {
                continue;
            }
            if decode(&tail[start..=index]).is_err() {
                break;
            }
            keep = from + index as u64 + 1;
            start = index + 1;
        }
        if keep != size {
            file.set_len(keep)?;
        }
        cache.validated = keep;
        cache.confirmed = true;
        cache.last_start = last_record_start(&mut file, keep)?;
        Ok(())
    }

    fn read(&self, keep: impl Fn(&Entry) -> bool) -> Result<Vec<Entry>, Error> {
        let mut cache = self.cache();
        let mut text = Vec::new();
        match File::open(&self.path) {
            Ok(mut file) => file.read_to_end(&mut text)?,
            Err(err) if err.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
            Err(err) => return Err(err.into()),
        };
        let mut entries = Vec::new();
        for line in text.split_inclusive(|byte| *byte == b'\n') {
            if !line.ends_with(b"\n") {
                *cache = Cache::default();
                return Err(Error::Torn(format!(
                    "{} bytes of a record with no line end",
                    line.len()
                )));
            }
            let entry = decode(line).inspect_err(|_| *cache = Cache::default())?;
            if keep(&entry) {
                entries.push(entry);
            }
        }
        Ok(entries)
    }
}

impl Store for FileStore {
    fn append(&self, entry: Entry) -> Result<(), Error> {
        let payload = serde_json::to_vec(&entry).map_err(|err| Error::Torn(err.to_string()))?;
        let line = frame(&payload);
        let mut cache = self.cache();
        self.repair(&mut cache)?;
        let mut file = OpenOptions::new().append(true).open(&self.path)?;
        file.write_all(&line)?;
        file.sync_all()?;
        Ok(())
    }

    fn history(&self, session_id: &str) -> Result<Vec<Entry>, Error> {
        self.read(|entry| entry.record.session_id == session_id)
    }

    fn sessions(&self) -> Result<Vec<Entry>, Error> {
        Ok(sessions(&self.read(|_| true)?))
    }
}

fn read_from(file: &mut File, start: u64, end: u64) -> Result<Vec<u8>, Error> {
    let mut buffer = Vec::new();
    file.seek(SeekFrom::Start(start))?;
    Read::by_ref(file)
        .take(end.saturating_sub(start))
        .read_to_end(&mut buffer)?;
    Ok(buffer)
}

fn read_line_at(file: &mut File, start: u64, size: u64) -> Result<Vec<u8>, Error> {
    let mut line = Vec::new();
    let mut chunk = [0u8; 512];
    file.seek(SeekFrom::Start(start))?;
    let mut offset = start;
    while offset < size {
        let read = file.read(&mut chunk)?;
        if read == 0 {
            break;
        }
        offset += read as u64;
        if let Some(end) = chunk[..read].iter().position(|byte| *byte == b'\n') {
            line.extend_from_slice(&chunk[..=end]);
            return Ok(line);
        }
        line.extend_from_slice(&chunk[..read]);
    }
    Ok(line)
}

fn last_record_start(file: &mut File, size: u64) -> Result<u64, Error> {
    let window = size.min(4096);
    let buffer = read_from(file, size - window, size)?;
    Ok((1..buffer.len())
        .rev()
        .find(|index| buffer[index - 1] == b'\n')
        .map_or(0, |index| size - window + index as u64))
}

pub fn frame(payload: &[u8]) -> Vec<u8> {
    let mut line = format!("{:08x} ", crc32(payload)).into_bytes();
    line.extend_from_slice(payload);
    line.push(b'\n');
    line
}

pub(crate) fn unframe(line: &[u8]) -> Result<&[u8], Error> {
    let line = line.strip_suffix(b"\n").unwrap_or(line);
    let at = line
        .iter()
        .position(|byte| *byte == b' ')
        .ok_or_else(|| Error::Torn("a record with no checksum".into()))?;
    let (sum, payload) = (&line[..at], &line[at + 1..]);
    if format!("{:08x}", crc32(payload)).as_bytes() != sum {
        return Err(Error::Torn(
            "a record whose checksum does not match its bytes".into(),
        ));
    }
    Ok(payload)
}

fn decode(line: &[u8]) -> Result<Entry, Error> {
    serde_json::from_slice(unframe(line)?).map_err(|err| Error::Torn(err.to_string()))
}

fn crc32(bytes: &[u8]) -> u32 {
    let mut crc = !0u32;
    for byte in bytes {
        crc ^= u32::from(*byte);
        for _ in 0..8 {
            crc = (crc >> 1) ^ (0xEDB8_8320 & (crc & 1).wrapping_neg());
        }
    }
    !crc
}
