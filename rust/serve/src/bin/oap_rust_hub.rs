use std::path::Path;
use std::process::ExitCode;
use std::sync::Arc;

use oap_binding::FileStore;
use oap_serve::endpoint::{Endpoint, EndpointOptions};
use oap_serve::stdio::{serve, StdioOptions};
use oap_serve::{Hub, HubOptions, Registry};

const USAGE: &str = "usage: oap-rust-hub serve --stdio [--session-history=PATH] serves many sessions; oap-rust-hub serve agent [--backend=B] [--stdio] [--session-history=PATH] serves one";

fn main() -> ExitCode {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let Some(("serve", flags)) = args.split_first().map(|(verb, rest)| (verb.as_str(), rest))
    else {
        eprintln!("{USAGE}");
        return ExitCode::from(2);
    };
    let (agent, flags) = match flags.split_first() {
        Some((role, rest)) if role == "agent" => (true, rest),
        _ => (false, flags),
    };
    let mut stdio = agent;
    let mut history = None;
    let mut backend = "memory".to_owned();
    for flag in flags {
        if flag == "--stdio" {
            stdio = true;
        } else if let Some(path) = flag.strip_prefix("--session-history=") {
            history = Some(path.to_owned());
        } else if let Some(name) = flag.strip_prefix("--backend=").filter(|_| agent) {
            backend = name.to_owned();
        } else {
            eprintln!("oap-rust-hub: unknown flag {flag:?}\n{USAGE}");
            return ExitCode::from(2);
        }
    }
    if !stdio {
        eprintln!("{USAGE}");
        return ExitCode::from(2);
    }
    let mut options = HubOptions::default();
    if let Some(path) = history.filter(|path| !path.is_empty()) {
        match FileStore::open(Path::new(&path)) {
            Ok(store) => options.bindings = Some(Arc::new(store)),
            Err(err) => {
                eprintln!("oap-rust-hub: session history {path:?}: {err}");
                return ExitCode::FAILURE;
            }
        }
    }
    let hub = Hub::with_options(Registry::default_registry(), options);
    let input = std::io::BufReader::new(std::io::stdin());
    let served = if agent {
        Endpoint::new(
            &hub,
            EndpointOptions {
                adapter: backend,
                ..EndpointOptions::default()
            },
        )
        .and_then(|endpoint| endpoint.run(input, std::io::stdout()))
        .map_err(|err| err.to_string())
    } else {
        serve(&hub, input, std::io::stdout(), &StdioOptions::default())
            .map_err(|err| err.to_string())
    };
    for (session, err) in hub.close_sessions() {
        eprintln!("oap-rust-hub: close session {}: {err}", session.as_str());
    }
    match served {
        Ok(()) => ExitCode::SUCCESS,
        Err(err) => {
            eprintln!("oap-rust-hub: {err}");
            ExitCode::FAILURE
        }
    }
}
