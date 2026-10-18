mod scan;

use std::fs;
use std::path::Path;
use std::process::{exit, Command};

fn main() {
    let mut mode = None;
    let mut files = Vec::new();
    for arg in std::env::args().skip(1) {
        match arg.as_str() {
            "--check" | "--write" | "--stats" if mode.is_none() => mode = Some(arg),
            "--check" | "--write" | "--stats" => usage(),
            _ if arg.starts_with('-') => usage(),
            _ => files.push(arg),
        }
    }
    let Some(mode) = mode else { usage() };
    if files.is_empty() {
        files = tracked().unwrap_or_else(|err| fail(&err));
    }
    exit(match mode.as_str() {
        "--write" => write(&files),
        "--stats" => {
            report(&files);
            0
        }
        _ => {
            if report(&files) > 0 {
                1
            } else {
                0
            }
        }
    })
}

fn usage() -> ! {
    eprintln!("nocomment: choose exactly one of --check, --write, --stats, then optional paths");
    exit(2)
}

fn fail(message: &str) -> ! {
    eprintln!("nocomment: {message}");
    exit(2)
}

fn tracked() -> Result<Vec<String>, String> {
    let out = Command::new("git")
        .args(["ls-files", "-z", "--", "*.rs"])
        .output()
        .map_err(|err| format!("git ls-files: {err}"))?;
    if !out.status.success() {
        return Err(format!(
            "git ls-files: {}",
            String::from_utf8_lossy(&out.stderr).trim()
        ));
    }
    Ok(String::from_utf8_lossy(&out.stdout)
        .split('\0')
        .filter(|path| !path.is_empty())
        .map(str::to_owned)
        .collect())
}

fn read(path: &str) -> String {
    fs::read_to_string(path).unwrap_or_else(|err| fail(&format!("{path}: {err}")))
}

fn report(files: &[String]) -> usize {
    let (mut total, mut commented) = (0, 0);
    for path in files {
        let count = scan::comments(&read(path))
            .unwrap_or_else(|err| fail(&format!("{path}: {err:?}")))
            .len();
        if count == 0 {
            continue;
        }
        println!("{path}: {count}");
        total += count;
        commented += 1;
    }
    eprintln!("nocomment: {total} comment(s) in {commented} file(s)");
    commented
}

fn write(files: &[String]) -> i32 {
    let mut pending = Vec::new();
    for path in files {
        let meta = fs::symlink_metadata(path).unwrap_or_else(|err| fail(&format!("{path}: {err}")));
        if meta.file_type().is_symlink() {
            fail(&format!("refusing to write through symlink: {path}"));
        }
        let src = read(path);
        let out = scan::strip(&src).unwrap_or_else(|err| fail(&format!("{path}: {err:?}")));
        if out != src {
            pending.push((path, out, meta.permissions()));
        }
    }
    for (path, out, permissions) in pending {
        let staged = Path::new(path).with_extension("rs.nocomment");
        let result = fs::write(&staged, out)
            .and_then(|()| fs::set_permissions(&staged, permissions))
            .and_then(|()| fs::rename(&staged, path));
        if let Err(err) = result {
            let _ = fs::remove_file(&staged);
            fail(&format!("{path}: {err}"));
        }
        println!("{path}");
    }
    0
}
