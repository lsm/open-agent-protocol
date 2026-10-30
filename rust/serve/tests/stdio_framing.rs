use oap_serve::stdio::{serve, StdioError, StdioOptions};
use oap_serve::{Hub, Registry};
use serde_json::Value;

fn run(input: &str, options: StdioOptions) -> (Vec<Value>, Result<(), StdioError>) {
    let hub = Hub::new(Registry::default_registry());
    let mut output = Vec::new();
    let result = serve(&hub, input.as_bytes(), &mut output, &options);
    let lines = String::from_utf8(output)
        .unwrap()
        .lines()
        .map(|line| serde_json::from_str(line).unwrap())
        .collect();
    (lines, result)
}

fn ok(input: &str) -> Vec<Value> {
    let (lines, result) = run(input, StdioOptions::default());
    result.unwrap();
    lines
}

#[test]
fn the_adapters_op_lists_the_registry() {
    let lines = ok("{\"id\":7,\"op\":\"adapters\"}\n");
    assert_eq!(lines.len(), 1);
    let response = &lines[0];
    assert_eq!(response["id"], 7);
    assert_eq!(response["ok"], true);
    let adapters = response["result"]["adapters"].as_array().unwrap();
    assert_eq!(adapters.len(), 1);
    assert_eq!(adapters[0]["name"], "memory");
    assert_eq!(
        adapters[0]["capabilities"]["endpoint"]["id"],
        "reference.memory"
    );
    assert!(adapters[0]["capability_revision"].is_string());
}

#[test]
fn a_response_line_carries_its_members_in_go_order() {
    let hub = Hub::new(Registry::default_registry());
    let mut output = Vec::new();
    serve(
        &hub,
        "{\"id\":1,\"op\":\"bogus\"}\n".as_bytes(),
        &mut output,
        &StdioOptions::default(),
    )
    .unwrap();
    assert_eq!(
        String::from_utf8(output).unwrap(),
        "{\"id\":1,\"ok\":false,\"result\":null,\"error\":{\"code\":\"unknown_op\",\"message\":\"no op \\\"bogus\\\"\"}}\n"
    );
}

#[test]
fn an_op_refuses_a_parameter_it_does_not_take() {
    for line in [
        r#"{"id":1,"op":"adapters","session_id":"s"}"#,
        r#"{"id":1,"op":"adapters","adapter":""}"#,
        r#"{"id":1,"op":"adapters","after":null}"#,
    ] {
        let lines = ok(&format!("{line}\n"));
        assert_eq!(lines[0]["error"]["code"], "invalid_request", "{line}");
    }
    let lines = ok("{\"id\":1,\"op\":\"adapters\",\"session_id\":\"s\",\"run_id\":\"r\"}\n");
    assert_eq!(
        lines[0]["error"]["message"],
        "op \"adapters\" accepts no session_id, run_id parameter"
    );
}

#[test]
fn an_unknown_op_is_a_request_error_and_serving_goes_on() {
    let lines = ok("{\"id\":1,\"op\":\"bogus\"}\n{\"id\":2,\"op\":\"adapters\"}\n");
    assert_eq!(lines[0]["error"]["code"], "unknown_op");
    assert_eq!(lines[1]["ok"], true);
}

#[test]
fn a_malformed_line_ends_serving_after_the_lines_before_it_are_answered() {
    let cases = [
        "not json\n",
        "[1,2]\n",
        "\"adapters\"\n",
        "7\n",
        "{\"id\":2}\n",
        "{\"op\":\"adapters\"}\n",
        "{\"id\":null,\"op\":\"adapters\"}\n",
        "{\"id\":\"2\",\"op\":\"adapters\"}\n",
        "{\"id\":2.5,\"op\":\"adapters\"}\n",
        "{\"id\":2,\"op\":\"adapters\",\"extra\":1}\n",
        "{\"id\":2,\"op\":\"adapters\"} {\"id\":3}\n",
        "{\"id\":2,\"op\":\"adapters\"}}\n",
        "{\"id\":1,\"id\":2,\"op\":\"adapters\"}\n",
        "{\"id\":1,\"ID\":2,\"op\":\"adapters\"}\n",
        "{\"id\":1,\"op\":\"adapters\",\"Adapter\":\"ignored\"}\n",
        "\n",
        "{\"id\":2,\"op\":\"adapters\"}\r\n",
        "{\"id\":2,\"op\":\"adapters\"",
        "{\"id\":2,\"op\":\"adapters\",\"adapter\":7}\n",
        "{\"id\":2,\"op\":\"state\",\"session_id\":[\"a\"]}\n",
        "{\"id\":2,\"op\":\"open\",\"allow_degraded_features\":\"a\"}\n",
        "{\"id\":2,\"op\":\"state\",\"run_id\":true}\n",
    ];
    for case in cases {
        let input = format!("{{\"id\":1,\"op\":\"adapters\"}}\n{case}");
        let (lines, result) = run(&input, StdioOptions::default());
        assert_eq!(lines.len(), 1, "{case:?}");
        assert_eq!(lines[0]["id"], 1);
        match result {
            Err(StdioError::MalformedLine { line, detail }) => {
                assert_eq!(line, 2, "{case:?}");
                assert!(!detail.is_empty());
            }
            other => panic!("{case:?}: {other:?}"),
        }
    }
    let mut invalid = b"{\"id\":1,\"op\":\"adapters\"}\n{\"id\":2,\"op\":\"".to_vec();
    invalid.extend_from_slice(b"\xff\"}\n");
    let hub = Hub::new(Registry::default_registry());
    let result = serve(&hub, &invalid[..], Vec::new(), &StdioOptions::default());
    assert!(matches!(
        result,
        Err(StdioError::MalformedLine { line: 2, .. })
    ));
}

#[test]
fn a_line_over_the_frame_limit_ends_serving_and_a_limit_below_the_floor_is_refused() {
    let padded = format!(
        "{{\"id\":1,\"op\":\"adapters\"}}\n{{\"id\":2,\"op\":\"adapters\",\"pad\":\"{}\"}}\n",
        "x".repeat(7000)
    );
    let (lines, result) = run(&padded, StdioOptions { frame_limit: 6000 });
    assert_eq!(lines.len(), 1);
    assert!(matches!(
        result,
        Err(StdioError::MalformedLine { line: 2, .. })
    ));
    let (_, result) = run("", StdioOptions { frame_limit: 255 });
    assert!(matches!(result, Err(StdioError::FrameLimit(255))));
}

#[test]
fn an_empty_input_ends_cleanly() {
    assert!(ok("").is_empty());
}

#[test]
fn a_response_over_the_frame_limit_is_refused_and_serving_goes_on() {
    let (lines, result) = run(
        "{\"id\":1,\"op\":\"adapters\"}\n{\"id\":2,\"op\":\"adapters\",\"session_id\":\"none\"}\n",
        StdioOptions { frame_limit: 1024 },
    );
    result.unwrap();
    assert_eq!(lines[0]["id"], 1);
    assert_eq!(lines[0]["error"]["code"], "response_too_large");
    assert_eq!(lines[1]["error"]["code"], "invalid_request");
}

#[test]
fn an_adapter_whose_descriptor_carries_no_revision_is_listed_with_an_error() {
    let mut registry = Registry::default_registry();
    registry
        .register("bare", std::sync::Arc::new(Bare))
        .unwrap();
    let hub = Hub::new(registry);
    let mut output = Vec::new();
    serve(
        &hub,
        "{\"id\":1,\"op\":\"adapters\"}\n".as_bytes(),
        &mut output,
        &StdioOptions::default(),
    )
    .unwrap();
    let response: Value = serde_json::from_slice(&output).unwrap();
    let bare = &response["result"]["adapters"][0];
    assert_eq!(bare["name"], "bare");
    assert_eq!(
        bare["error"],
        "adapter descriptor carries no capability revision"
    );
    assert!(bare.get("capabilities").is_none());
}

struct Bare;

impl oap_adapter::Adapter for Bare {
    fn probe(&self) -> oap_adapter::BoxFuture<'_, oap_adapter::Descriptor> {
        Box::pin(async { Ok(oap_adapter::Descriptor::default()) })
    }

    fn open(
        &self,
        _: oap_adapter::OpenRequest,
    ) -> oap_adapter::BoxFuture<'_, Box<dyn oap_adapter::Session>> {
        Box::pin(async { Err(oap_adapter::Error::SessionClosed) })
    }
}

#[test]
fn a_raw_parameter_keeps_any_valid_json_number() {
    let lines = ok("{\"id\":1,\"op\":\"bogus\",\"request\":1e400,\"after\":1e400}\n");
    assert_eq!(lines[0]["error"]["code"], "unknown_op");
}
