fn windows_separator(byte: u8) -> bool {
    byte == b'/' || byte == b'\\'
}

fn separator(byte: u8, windows: bool) -> bool {
    byte == b'/' || (windows && byte == b'\\')
}

struct Buffer<'a> {
    path: &'a [u8],
    buf: Option<Vec<u8>>,
    w: usize,
}

impl Buffer<'_> {
    fn index(&self, index: usize) -> u8 {
        self.buf.as_ref().map_or(self.path[index], |buf| buf[index])
    }

    fn append(&mut self, byte: u8) {
        let w = self.w;
        self.w += 1;
        let path = self.path;
        let buf = match &mut self.buf {
            Some(buf) => buf,
            None if w < path.len() && path[w] == byte => return,
            None => {
                let mut buf = vec![0; path.len()];
                buf[..w].copy_from_slice(&path[..w]);
                self.buf.insert(buf)
            }
        };
        if w < buf.len() {
            buf[w] = byte;
        } else {
            buf.push(byte);
        }
    }

    fn prepend(&mut self, prefix: [u8; 2]) {
        if let Some(buf) = &mut self.buf {
            buf.splice(0..0, prefix);
            self.w += prefix.len();
        }
    }
}

fn prefix_fold(path: &[u8], prefix: &[u8]) -> bool {
    path.len() >= prefix.len()
        && path.iter().zip(prefix).all(|(&byte, &wanted)| {
            if windows_separator(wanted) {
                windows_separator(byte)
            } else {
                byte.eq_ignore_ascii_case(&wanted)
            }
        })
        && path
            .get(prefix.len())
            .is_none_or(|&byte| windows_separator(byte))
}

fn unc_len(path: &[u8], prefix_len: usize) -> usize {
    path.iter()
        .enumerate()
        .skip(prefix_len)
        .filter(|(_, &byte)| windows_separator(byte))
        .nth(1)
        .map_or(path.len(), |(index, _)| index)
}

fn valid_volume_len(path: &[u8], len: usize) -> usize {
    if path[..len]
        .split(|&byte| windows_separator(byte))
        .any(|part| part == b"..")
    {
        0
    } else {
        len
    }
}

fn volume_len(path: &[u8]) -> usize {
    if path.len() >= 2 && path[1] == b':' {
        return 2;
    }
    if path.first().is_none_or(|&byte| !windows_separator(byte)) {
        return 0;
    }
    if prefix_fold(path, b"\\\\.") || prefix_fold(path, b"\\\\?") || prefix_fold(path, b"\\??") {
        if path.len() == 3 {
            return 3;
        }
        if prefix_fold(&path[4..], b"UNC") {
            return valid_volume_len(path, unc_len(path, 8));
        }
        return match path[4..].iter().position(|&byte| windows_separator(byte)) {
            Some(index) => valid_volume_len(path, 4 + index),
            None => valid_volume_len(path, path.len()),
        };
    }
    if path.len() >= 2 && windows_separator(path[1]) {
        return valid_volume_len(path, unc_len(path, 2));
    }
    0
}

fn post_clean(out: &mut Buffer<'_>) {
    let Some(buf) = &out.buf else {
        return;
    };
    let head = buf
        .iter()
        .find(|&&byte| windows_separator(byte) || byte == b':');
    if head == Some(&b':') {
        out.prepend(*b".\\");
    } else if buf.len() >= 3 && windows_separator(buf[0]) && buf[1] == b'?' && buf[2] == b'?' {
        out.prepend(*b"\\.");
    }
}

fn from_slash(path: &str, windows: bool) -> String {
    if windows {
        path.replace('/', "\\")
    } else {
        path.to_owned()
    }
}

pub fn clean(original: &str, windows: bool) -> String {
    let bytes = original.as_bytes();
    let main = if windows { b'\\' } else { b'/' };
    let volume = if windows { volume_len(bytes) } else { 0 };
    let path = &bytes[volume..];
    if path.is_empty() {
        if volume > 1 && windows_separator(bytes[0]) && windows_separator(bytes[1]) {
            return from_slash(original, windows);
        }
        return format!("{original}.");
    }
    let rooted = separator(path[0], windows);
    let n = path.len();
    let mut out = Buffer {
        path,
        buf: None,
        w: 0,
    };
    let (mut r, mut dotdot) = (0, 0);
    if rooted {
        out.append(main);
        (r, dotdot) = (1, 1);
    }
    while r < n {
        let ends = |at: usize| at == n || separator(path[at], windows);
        if separator(path[r], windows) || (path[r] == b'.' && ends(r + 1)) {
            r += 1;
        } else if path[r] == b'.' && path[r + 1] == b'.' && ends(r + 2) {
            r += 2;
            if out.w > dotdot {
                out.w -= 1;
                while out.w > dotdot && !separator(out.index(out.w), windows) {
                    out.w -= 1;
                }
            } else if !rooted {
                if out.w > 0 {
                    out.append(main);
                }
                out.append(b'.');
                out.append(b'.');
                dotdot = out.w;
            }
        } else {
            if out.w != usize::from(rooted) {
                out.append(main);
            }
            while r < n && !separator(path[r], windows) {
                out.append(path[r]);
                r += 1;
            }
        }
    }
    if out.w == 0 {
        out.append(b'.');
    }
    if windows && volume == 0 {
        post_clean(&mut out);
    }
    let mut cleaned = bytes[..volume].to_vec();
    match &out.buf {
        Some(buf) => cleaned.extend_from_slice(&buf[..out.w]),
        None => cleaned.extend_from_slice(&path[..out.w]),
    }
    from_slash(&String::from_utf8_lossy(&cleaned), windows)
}
