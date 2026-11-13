#![cfg(unix)]

use std::path::{Path, PathBuf};
use std::sync::{Mutex, MutexGuard};
use std::time::{Duration, Instant};

use oap_adapter::{Error, NativeListRequest};
use oap_adapter_hermes::adapter::{Adapter, Config};
use oap_adapter_hermes::process::ProcessConfig;

const READY: &str = r#"{"jsonrpc":"2.0","method":"event","params":{"type":"gateway.ready","payload":{"skin":{},"change_events":true,"replay_epoch":"0123456789abcdef0123456789abcdef"}}}"#;
const EVENT: &str = r#"{"jsonrpc":"2.0","method":"event","params":{"type":"message.start","session_id":"sess0001","seq":1}}"#;

static SPAWNING: Mutex<()> = Mutex::new(());

fn serial() -> MutexGuard<'static, ()> {
    SPAWNING.lock().unwrap_or_else(|poison| poison.into_inner())
}

fn probe(name: &str) -> PathBuf {
    let dir = std::env::temp_dir()
        .join(format!("oap-hermes-kept-{}", std::process::id()))
        .join(name);
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    dir.join("gateway")
}

fn script() -> String {
    format!(
        r#"set -u
n=$(( $(cat "$0.n" 2>/dev/null || echo 0) + 1 ))
echo "$n" > "$0.n"
printf '%s\n' '{READY}'
i=0
while IFS= read -r line; do
  i=$((i + 1))
  printf '%s %s\n' "$n" "$line" >> "$0.log"
  id=$(printf '%s' "$line" | sed 's/^{{"id":\([0-9]*\),.*/\1/')
  case $(sed -n "${{i}}p" "$0.plan.$n" 2>/dev/null) in
    remote) printf '{{"jsonrpc":"2.0","id":%s,"error":{{"code":-32000,"message":"busy"}}}}\n' "$id" ;;
    die) exit 0 ;;
    flood)
      j=0
      while [ "$j" -lt 300 ]; do printf '%s\n' '{EVENT}'; j=$((j + 1)); done
      printf '{{"jsonrpc":"2.0","id":%s,"result":{{"sessions":[{{"id":"stored-%s"}}]}}}}\n' "$id" "$n" ;;
    hold)
      while [ ! -e "$0.go" ]; do sleep 0.01; done
      printf '{{"jsonrpc":"2.0","id":%s,"result":{{"sessions":[{{"id":"stored-%s"}}]}}}}\n' "$id" "$n" ;;
    *) printf '{{"jsonrpc":"2.0","id":%s,"result":{{"sessions":[{{"id":"stored-%s"}}]}}}}\n' "$id" "$n" ;;
  esac
done
if [ -e "$0.linger.$n" ]; then
  trap 'printf closed > "$0.closed.$n"; exit 0' INT TERM
  k=0
  while [ "$k" -lt 100 ]; do sleep 0.05; k=$((k + 1)); done
  exit 0
fi
printf closed > "$0.closed.$n"
"#
    )
}

fn adapter(probe: &Path, plans: &[&[&str]]) -> Adapter {
    for (at, plan) in plans.iter().enumerate() {
        let mut text = plan.join("\n");
        text.push('\n');
        std::fs::write(format!("{}.plan.{}", probe.display(), at + 1), text).unwrap();
    }
    Adapter::new(Config {
        process: ProcessConfig {
            path: "/bin/sh".into(),
            args: vec!["-c".into(), script(), probe.to_string_lossy().into_owned()],
            env: Some(vec!["PATH=/usr/bin:/bin".into()]),
            exit_timeout: Some(Duration::from_secs(1)),
            ..ProcessConfig::default()
        },
        model: "hermes-test".into(),
        ..Config::default()
    })
    .unwrap()
}

fn linger(probe: &Path, launch: usize) {
    std::fs::write(format!("{}.linger.{launch}", probe.display()), "").unwrap();
}

const LINGERED: &str = "hermes rpc: gateway did not exit on stdin EOF";

fn list(adapter: &Adapter, limit: usize) -> Result<Vec<String>, Error> {
    adapter
        .native_list(NativeListRequest {
            limit,
            ..NativeListRequest::default()
        })
        .map(|listed| listed.into_iter().map(|row| row.native_id).collect())
}

fn launches(probe: &Path) -> usize {
    std::fs::read_to_string(format!("{}.n", probe.display()))
        .map_or(0, |text| text.trim().parse().unwrap())
}

fn sent(probe: &Path) -> Vec<String> {
    std::fs::read_to_string(format!("{}.log", probe.display()))
        .unwrap_or_default()
        .lines()
        .map(str::to_owned)
        .collect()
}

fn closed(probe: &Path, launch: usize) -> bool {
    Path::new(&format!("{}.closed.{launch}", probe.display())).exists()
}

fn await_closed(probe: &Path, launch: usize) -> bool {
    let until = Instant::now() + Duration::from_secs(5);
    while !closed(probe, launch) && Instant::now() < until {
        std::thread::sleep(Duration::from_millis(10));
    }
    closed(probe, launch)
}

#[test]
fn the_native_list_asks_one_kept_gateway_and_closing_the_adapter_stops_it() {
    let _serial = serial();
    let probe = probe("kept");
    let adapter = adapter(&probe, &[&["flood"]]);
    linger(&probe, 1);
    assert_eq!(list(&adapter, 2), Ok(vec!["stored-1".to_owned()]));
    assert_eq!(
        sent(&probe),
        [r#"1 {"id":1,"jsonrpc":"2.0","method":"session.list","params":{"limit":2}}"#]
    );
    assert_eq!(launches(&probe), 1);
    assert!(!closed(&probe, 1));
    assert_eq!(adapter.close(), Err(Error::Harness(LINGERED.into())));
    assert!(closed(&probe, 1));
    assert_eq!(adapter.close(), Ok(()));
}

#[test]
fn the_listings_share_one_gateway_and_start_another_only_when_it_breaks() {
    let _serial = serial();
    let probe = probe("share");
    let adapter = adapter(
        &probe,
        &[
            &["ok", "ok", "remote", "die"],
            &["ok", "die"],
            &["die"],
            &["die"],
            &["ok"],
        ],
    );
    assert_eq!(list(&adapter, 1), Ok(vec!["stored-1".to_owned()]));
    assert_eq!(list(&adapter, 1), Ok(vec!["stored-1".to_owned()]));
    assert_eq!(launches(&probe), 1);
    assert_eq!(
        list(&adapter, 1),
        Err(Error::Harness(
            "hermes rpc error -32000 for request 3: busy".into()
        ))
    );
    assert_eq!(launches(&probe), 1);
    assert_eq!(list(&adapter, 1), Ok(vec!["stored-2".to_owned()]));
    assert_eq!(launches(&probe), 2);
    assert!(list(&adapter, 1).is_err());
    assert_eq!(launches(&probe), 3);
    assert!(list(&adapter, 1).is_err());
    assert_eq!(launches(&probe), 4);
    assert_eq!(list(&adapter, 1), Ok(vec!["stored-5".to_owned()]));
    let by_launch: Vec<usize> = sent(&probe)
        .iter()
        .map(|line| line.split(' ').next().unwrap().parse().unwrap())
        .collect();
    assert_eq!(by_launch, [1, 1, 1, 1, 2, 2, 3, 4, 5]);
    assert_eq!(adapter.close(), Ok(()));
    assert!(await_closed(&probe, 5));
}

#[test]
fn a_listing_while_the_kept_gateway_is_busy_runs_on_a_gateway_of_its_own() {
    let _serial = serial();
    let probe = probe("busy");
    let adapter = adapter(&probe, &[&["hold", "ok"], &["ok"]]);
    linger(&probe, 2);
    std::thread::scope(|scope| {
        let held = scope.spawn(|| list(&adapter, 1));
        let until = Instant::now() + Duration::from_secs(5);
        while sent(&probe).is_empty() && Instant::now() < until {
            std::thread::sleep(Duration::from_millis(10));
        }
        assert_eq!(list(&adapter, 1), Ok(vec!["stored-2".to_owned()]));
        assert!(closed(&probe, 2));
        std::fs::write(format!("{}.go", probe.display()), "").unwrap();
        assert_eq!(held.join().unwrap(), Ok(vec!["stored-1".to_owned()]));
    });
    assert_eq!(list(&adapter, 1), Ok(vec!["stored-1".to_owned()]));
    assert_eq!(launches(&probe), 2);
    assert_eq!(adapter.close(), Ok(()));
    assert!(await_closed(&probe, 1));
}

#[test]
fn a_gateway_that_cannot_start_fails_the_listing() {
    let adapter = Adapter::new(Config {
        process: ProcessConfig {
            path: "/nonexistent/hermes".into(),
            ..ProcessConfig::default()
        },
        model: "hermes-test".into(),
        ..Config::default()
    })
    .unwrap();
    assert!(matches!(list(&adapter, 1), Err(Error::Harness(_))));
    assert_eq!(adapter.close(), Ok(()));
}
