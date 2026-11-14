mod support;

#[test]
fn each_cancel_scenario_settles_as_go_does() {
    support::replay::replay(include_str!("go_cancels.json"), 9);
}
