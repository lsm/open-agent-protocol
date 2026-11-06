use std::time::{Duration, Instant};

use oap_adapter_acp::process::{Implementation, Process, ProcessConfig};

fn run() -> Result<(), String> {
    let mut args = std::env::args().skip(1);
    let config = ProcessConfig {
        path: args.next().ok_or("expected synthetic agent executable")?,
        args: args.collect(),
        env: Some(Vec::new()),
        shutdown_timeout: Some(Duration::from_millis(300)),
        client_info: Some(Implementation {
            name: "synthetic<&>".into(),
            version: "0.1".into(),
            ..Implementation::default()
        }),
        ..ProcessConfig::default()
    };
    let process = Process::start(&config, Some(Instant::now() + Duration::from_secs(2)))
        .map_err(|error| error.to_string())?;
    let closed = process.close();
    println!(
        "{}",
        serde_json::json!({
            "protocolVersion": process.initialize.protocol_version,
            "agentCapabilities": process.initialize.agent_capabilities,
            "authMethods": process.initialize.auth_methods,
            "agentInfo": process.initialize.agent_info,
            "stderr": process.stderr(),
            "closeError": closed.err(),
        })
    );
    Ok(())
}

fn main() {
    if let Err(error) = run() {
        eprintln!("{error}");
        std::process::exit(1);
    }
}
