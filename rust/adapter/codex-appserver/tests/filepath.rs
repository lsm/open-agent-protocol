use oap_adapter_codex_appserver::filepath::clean;

#[derive(serde::Deserialize)]
struct Case {
    path: String,
    unix: String,
    windows: String,
}

#[test]
fn clean_folds_each_path_as_go_does_on_unix_and_on_windows() {
    let cases: Vec<Case> = serde_json::from_str(include_str!("go_clean.json")).unwrap();
    assert_eq!(cases.len(), 39);
    let mut wrong = Vec::new();
    for case in &cases {
        for (windows, wanted) in [(false, &case.unix), (true, &case.windows)] {
            let cleaned = clean(&case.path, windows);
            if &cleaned != wanted {
                wrong.push(format!(
                    "{:?} windows={windows}: {cleaned:?}, Go {wanted:?}",
                    case.path
                ));
            }
        }
    }
    assert!(wrong.is_empty(), "{}", wrong.join("\n"));
}
