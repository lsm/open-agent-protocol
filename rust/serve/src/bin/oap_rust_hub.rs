use std::path::Path;
use std::process::ExitCode;
use std::sync::atomic::Ordering;
use std::sync::Arc;

use oap_binding::FileStore;
use oap_serve::endpoint::{Endpoint, EndpointOptions};
use oap_serve::http::{serve_http, HttpOptions};
use oap_serve::stdio::{serve, StdioOptions};
use oap_serve::{Hub, HubOptions, Registry};

const USAGE: &str = "usage: oap-rust-hub serve [--addr=A | --stdio] [--session-history=PATH] [--config=F] serves many sessions; oap-rust-hub serve agent [--backend=B] [--config=F] [--stdio] [--session-history=PATH] serves one";

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
    let mut config = None;
    let mut addr = None;
    let mut flags = flags.iter();
    while let Some(flag) = flags.next() {
        if flag == "--stdio" {
            stdio = true;
            continue;
        }
        let (name, value) = match flag.split_once('=') {
            Some((name, value)) => (name, Some(value.to_owned())),
            None => (flag.as_str(), None),
        };
        if !matches!(name, "--session-history" | "--config")
            && !(agent && name == "--backend")
            && !(!agent && name == "--addr")
        {
            eprintln!("oap-rust-hub: unknown flag {flag:?}\n{USAGE}");
            return ExitCode::from(2);
        }
        let Some(value) = value.or_else(|| flags.next().cloned()) else {
            eprintln!("oap-rust-hub: flag {name} needs a value\n{USAGE}");
            return ExitCode::from(2);
        };
        match name {
            "--session-history" => history = Some(value),
            "--config" => config = Some(value),
            "--addr" => addr = Some(value),
            _ => backend = value,
        }
    }
    if stdio && addr.is_some() {
        eprintln!("oap-rust-hub: serve --stdio takes no listen address; --addr and --stdio are mutually exclusive");
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
    let registry = match config.filter(|path| !path.is_empty()) {
        None => Registry::default_registry(),
        Some(path) => match Registry::load(Path::new(&path), |name| std::env::var(name).ok()) {
            Ok(registry) => registry,
            Err(err) => {
                eprintln!("oap-rust-hub: {err}");
                return ExitCode::FAILURE;
            }
        },
    };
    let hub = Hub::with_options(registry, options);
    let input = std::io::BufReader::new(std::io::stdin());
    let pending = Arc::new(std::sync::atomic::AtomicUsize::new(0));
    let served = if agent {
        Endpoint::new(
            &hub,
            EndpointOptions {
                adapter: backend,
                ..EndpointOptions::default()
            },
        )
        .map(Arc::new)
        .and_then(|endpoint| {
            let (signalled, writing) = (endpoint.clone(), pending.clone());
            let mut count = 0;
            let relayed = ctrlc::set_handler(move || {
                count += 1;
                if count > 2 {
                    signalled.signal(count);
                    return;
                }
                writing.fetch_add(1, Ordering::SeqCst);
                let (applying, written, seen) = (signalled.clone(), writing.clone(), count);
                std::thread::spawn(move || {
                    if let Some(answer) = applying.signal(seen) {
                        eprintln!("{}", signal_line(seen, &answer));
                    }
                    written.fetch_sub(1, Ordering::SeqCst);
                });
            });
            if let Err(err) = relayed {
                eprintln!("oap-rust-hub: signals are not relayed: {err}");
            }
            endpoint.run(input, std::io::stdout())
        })
        .map_err(|err| err.to_string())
    } else if stdio {
        serve(&hub, input, std::io::stdout(), &StdioOptions::default())
            .map_err(|err| err.to_string())
    } else {
        serve_on(&hub, &addr.unwrap_or_else(|| DEFAULT_ADDR.to_owned()))
    };
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(1);
    while pending.load(Ordering::SeqCst) > 0 && std::time::Instant::now() < deadline {
        std::thread::sleep(std::time::Duration::from_millis(5));
    }
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

const DEFAULT_ADDR: &str = "127.0.0.1:6270";

fn serve_on(hub: &Hub, addr: &str) -> Result<(), String> {
    let listener =
        std::net::TcpListener::bind(addr).map_err(|err| format!("listen on {addr}: {err}"))?;
    let bound = listener.local_addr().map_err(|err| err.to_string())?;
    println!("listening on http://{bound}");
    eprintln!(
        "oap-rust-hub: serving adapters: {} (restart kills all sessions)",
        hub.registry().names().join(", ")
    );
    let host = addr.rsplit_once(':').map_or(addr, |(host, _)| host);
    let host_allowlist = match host.trim_start_matches('[').trim_end_matches(']') {
        "localhost" | "127.0.0.1" | "::1" => ["localhost", "127.0.0.1", "::1"]
            .map(str::to_owned)
            .to_vec(),
        _ => Vec::new(),
    };
    serve_http(
        hub,
        listener,
        &HttpOptions {
            host_allowlist,
            ..HttpOptions::default()
        },
    )
    .map_err(|err| err.to_string())
}

fn signal_line(count: usize, answer: &oap_protocol::EndpointShutdownResponse) -> String {
    let active: Vec<String> = answer
        .active_runs
        .iter()
        .map(|run| format!("{}/{}", run.session_id.as_str(), run.run_id.as_str()))
        .collect();
    let listed = if active.is_empty() {
        "none".to_owned()
    } else {
        active.join(", ")
    };
    if count == 1 {
        format!(
            "oap-rust-hub: shutting down gracefully; still active: {listed}; {} queued run(s) lost; signal again to force",
            answer.lost.len()
        )
    } else {
        format!("oap-rust-hub: forced shutdown; cut off: {listed}")
    }
}
