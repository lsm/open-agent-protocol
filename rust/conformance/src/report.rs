use std::process::Command;
use std::time::Duration;

use oap_protocol::{Envelope, SessionId};
use oap_validation::{Diagnostic, Validator};
use serde::Serialize;

use crate::{framing_contract, Check, Client, ClientError, Runner};

#[derive(Debug, Default)]
pub struct Options {
    pub session_id: SessionId,
    pub model: String,
    pub line_deadline: Duration,
}

#[derive(Debug, Default, Serialize)]
pub struct Report {
    pub checks: Vec<Check>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub diagnostics: Vec<Diagnostic>,
    #[serde(skip)]
    pub trace: Vec<Envelope>,
    pub passed: bool,
}

pub fn run(command: &mut Command, options: Options) -> Result<Report, ClientError> {
    let mut client = Client::spawn(command, options.line_deadline)?;
    let mut runner = Runner::new(&mut client, options.session_id);
    runner.drive(&options.model);
    let mut checks = runner.into_checks();
    client.close_input();
    checks.push(match client.wait() {
        Ok(status) if status.success() => Check::passed("endpoint exits 0 after stdin EOF"),
        Ok(status) => Check::failed("endpoint exits 0 after stdin EOF", status.to_string()),
        Err(error) => Check::failed("endpoint exits after stdin EOF", error.to_string()),
    });
    let trace = client.transcript();
    drop(client);
    let mut report = Report {
        checks,
        trace,
        ..Report::default()
    };
    let validation = serde_json::to_vec(&report.trace)
        .map_err(|error| error.to_string())
        .and_then(|bytes| {
            Validator::new()
                .map(|validator| validator.validate(&bytes, "endpoint-conformance"))
                .map_err(|error| error.to_string())
        });
    const VALID: &str = "the exchange validates as an OAP trace";
    report.checks.push(match validation {
        Ok(result) => {
            let valid = result.valid();
            report.diagnostics = result.diagnostics;
            if valid {
                Check::passed(VALID)
            } else {
                Check::failed(VALID, format!("{} diagnostic(s)", report.diagnostics.len()))
            }
        }
        Err(error) => Check::failed(VALID, error),
    });
    report
        .checks
        .extend(framing_contract(command, options.line_deadline));
    report.passed = report.checks.iter().all(|check| check.passed);
    Ok(report)
}
