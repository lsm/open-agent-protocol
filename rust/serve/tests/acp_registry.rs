use std::process::Command;
use std::time::{SystemTime, UNIX_EPOCH};

#[test]
fn configured_acp_opens_through_the_public_cli() {
    if !cfg!(unix) {
        eprintln!("skipped: the ACP CLI journey requires POSIX process groups");
        return;
    }
    match Command::new("python3").arg("--version").output() {
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => {
            eprintln!("skipped: the ACP CLI journey requires python3");
            return;
        }
        result => assert!(result.unwrap().status.success()),
    }
    let stamp = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    let output =
        std::env::temp_dir().join(format!("oap-acp-registry-{}-{stamp}", std::process::id()));
    let script = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../.claude/skills/verify/scripts/rust-acp-registry.py");
    let observed = Command::new("python3")
        .arg(script)
        .args([
            "--binary",
            env!("CARGO_BIN_EXE_oap-rust-hub"),
            "--output-dir",
        ])
        .arg(&output)
        .output()
        .unwrap();
    let stderr = std::fs::read_to_string(output.join("stderr.txt")).unwrap_or_default();
    std::fs::remove_dir_all(&output).unwrap();
    assert!(
        observed.status.success(),
        "{}\n{}\n{stderr}",
        String::from_utf8_lossy(&observed.stdout),
        String::from_utf8_lossy(&observed.stderr)
    );
    println!("{}", String::from_utf8_lossy(&observed.stdout));
}
