use std::path::Path;
use std::process::ExitCode;
use std::sync::Arc;

use oap_binding::FileStore;
use oap_serve::stdio::{serve, StdioOptions};
use oap_serve::{Hub, HubOptions, Registry};

const USAGE: &str = "usage: oap-rust-hub serve --stdio [--session-history=PATH]";

fn main() -> ExitCode {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let Some(("serve", flags)) = args.split_first().map(|(verb, rest)| (verb.as_str(), rest))
    else {
        eprintln!("{USAGE}");
        return ExitCode::from(2);
    };
    let mut stdio = false;
    let mut history = None;
    for flag in flags {
        match flag.as_str() {
            "--stdio" => stdio = true,
            other => match other.strip_prefix("--session-history=") {
                Some(path) => history = Some(path.to_owned()),
                None => {
                    eprintln!("oap-rust-hub: unknown flag {other:?}\n{USAGE}");
                    return ExitCode::from(2);
                }
            },
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
    match serve(
        &hub,
        std::io::BufReader::new(std::io::stdin()),
        std::io::stdout(),
        &StdioOptions::default(),
    ) {
        Ok(()) => ExitCode::SUCCESS,
        Err(err) => {
            eprintln!("oap-rust-hub: {err}");
            ExitCode::FAILURE
        }
    }
}
