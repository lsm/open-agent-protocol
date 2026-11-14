#[cfg(unix)]
fn main() -> Result<std::process::ExitCode, Box<dyn std::error::Error>> {
    use std::process::{Command, ExitCode};

    use oxap_conformance::{run, Options};

    let mut args = std::env::args().skip(1);
    let Some(executable) = args.next() else {
        eprintln!("usage: endpoint_conformance COMMAND [ARG...]");
        return Ok(ExitCode::from(2));
    };
    let mut command = Command::new(executable);
    command.args(args);
    let report = run(&mut command, Options::default())?;
    println!("{}", serde_json::to_string_pretty(&report)?);
    Ok(if report.passed {
        ExitCode::SUCCESS
    } else {
        ExitCode::FAILURE
    })
}

#[cfg(not(unix))]
fn main() -> std::process::ExitCode {
    eprintln!("the Rust conformance process runner requires Unix");
    std::process::ExitCode::from(2)
}
