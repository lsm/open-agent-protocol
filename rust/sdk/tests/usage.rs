use futures::StreamExt;
use oap_sdk::{AgentEvent, Client, ClientBuilder, Error, ExecutionRequest, ProviderEvent};
use serde_json::{json, Value};

async fn observe(client: &Client, agent: bool, stream: bool) -> Result<Value, Error> {
    let request = ExecutionRequest::prompt("fixture/openai-responses@mock", "usage");
    if !stream {
        let response = if agent {
            client.agent().run(request).await?
        } else {
            client.provider().complete(request).await?
        };
        return Ok(serde_json::to_value(response.usage).expect("usage JSON"));
    }
    if agent {
        let mut events = Box::pin(client.agent().stream(request));
        while let Some(event) = events.next().await {
            if let AgentEvent::AgentEnd { usage, .. } = event? {
                return Ok(serde_json::to_value(usage).expect("usage JSON"));
            }
        }
    } else {
        let mut events = Box::pin(client.provider().stream(request));
        while let Some(event) = events.next().await {
            if let ProviderEvent::MessageEnd { usage, .. } = event? {
                return Ok(serde_json::to_value(usage).expect("usage JSON"));
            }
        }
    }
    panic!("stream ended without its terminal event");
}

async fn fixture(raw: &str) -> Client {
    ClientBuilder::new()
        .command(env!("CARGO_BIN_EXE_oap-protocol-fake"))
        .args([format!("--usage={raw}")])
        .env_clear()
        .connect()
        .await
        .expect("fixture connection")
}

#[tokio::test]
async fn usage_preserves_absence_measured_zero_partial_and_maximum_counts() {
    let cases = [
        ("absent", Value::Null),
        ("{}", json!({})),
        (r#"{"input_tokens":0}"#, json!({"input":0})),
        (r#"{"output_tokens":0}"#, json!({"output":0})),
        (
            r#"{"total_tokens":18446744073709551615}"#,
            json!({"total":u64::MAX}),
        ),
        (
            r#"{"input_tokens":18446744073709551615,"output_tokens":0}"#,
            json!({"input":u64::MAX,"output":0}),
        ),
        (
            r#"{"input_tokens":0,"output_tokens":0,"total_tokens":0,"cache_read_input_tokens":0,"cache_write_input_tokens":0,"reasoning_tokens":0}"#,
            json!({"input":0,"output":0,"total":0,"cache_read":0,"cache_write":0,"reasoning":0}),
        ),
    ];
    for (raw, expected) in cases {
        let client = fixture(raw).await;
        for agent in [false, true] {
            for stream in [false, true] {
                let actual = observe(&client, agent, stream).await.expect("valid usage");
                assert_eq!(
                    actual, expected,
                    "usage={raw}, agent={agent}, stream={stream}"
                );
            }
        }
        client.close().await;
    }
}

#[tokio::test]
async fn usage_refuses_negative_malformed_and_out_of_range_counts() {
    let mut cases = Vec::new();
    for field in [
        "input_tokens",
        "output_tokens",
        "total_tokens",
        "cache_read_input_tokens",
        "cache_write_input_tokens",
        "reasoning_tokens",
    ] {
        for count in [
            "null",
            "-1",
            "1.5",
            "true",
            r#""3""#,
            "{}",
            "[]",
            "18446744073709551616",
        ] {
            cases.push(format!(r#"{{"{field}":{count}}}"#));
        }
    }
    cases.extend([
        "null".to_owned(),
        "true".to_owned(),
        "[]".to_owned(),
        "3".to_owned(),
        r#""usage""#.to_owned(),
    ]);
    for raw in cases {
        let client = fixture(&raw).await;
        for agent in [false, true] {
            for stream in [false, true] {
                let error = observe(&client, agent, stream)
                    .await
                    .expect_err("invalid usage must fail");
                assert_eq!(
                    error.code(),
                    Some("malformed_response"),
                    "usage={raw}, agent={agent}, stream={stream}: {error}"
                );
            }
        }
        client.close().await;
    }
}
