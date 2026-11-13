mod support;

#[test]
fn each_run_reports_the_token_usage_go_reports() {
    support::replay::replay(include_str!("go_usage.json"), 11);
}
