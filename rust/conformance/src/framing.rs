use std::process::Command;
use std::time::Duration;

use crate::{Check, Client};

pub fn framing_contract(command: &mut Command, deadline: Duration) -> [Check; 2] {
    [
        fatal_line(
            command,
            deadline,
            "a malformed line ends the endpoint non-zero",
            "this is not an envelope",
        ),
        fatal_line(
            command,
            deadline,
            "an envelope with no id ends the endpoint non-zero",
            r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.request"}"#,
        ),
    ]
}

fn fatal_line(command: &mut Command, deadline: Duration, name: &str, line: &str) -> Check {
    let result = Client::spawn(command, deadline).and_then(|mut client| {
        client.send_line(line)?;
        client.close_input();
        client.wait()
    });
    match result {
        Ok(status) if !status.success() => Check::passed(name),
        Ok(_) => Check::failed(name, "the endpoint exited 0 after this fatal input"),
        Err(error) => Check::failed(name, error.to_string()),
    }
}
