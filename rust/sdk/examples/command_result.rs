use futures::StreamExt;
use oap_sdk::{AgentEvent, ClientBuilder, ExecutionRequest};
use serde_json::json;

#[tokio::main(flavor = "current_thread")]
async fn main() {
    let args: Vec<String> = std::env::args().collect();
    let client = ClientBuilder::new()
        .command(&args[1])
        .args(["serve", "agent", "--backend", "codex", "--config", &args[2]])
        .connect()
        .await
        .expect("connect");
    let mut events = Box::pin(client.agent().stream(ExecutionRequest::prompt(
        "fixture/openai-responses@glm-test",
        "read the code",
    )));
    let mut ends = Vec::new();
    while let Some(event) = events.next().await {
        if let AgentEvent::ToolExecutionEnd {
            tool_call_id,
            is_error,
            ..
        } = event.expect("event")
        {
            ends.push(json!({"tool_call_id": tool_call_id, "is_error": is_error}));
        }
    }
    client.close().await;
    println!("{}", json!(ends));
    assert_eq!(ends.len(), 8);
    for (i, event) in ends.iter().enumerate() {
        assert_eq!(
            event["is_error"],
            json!(i != 0 && i != 5),
            "command outcome {i}"
        );
    }
}
