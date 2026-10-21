use futures::StreamExt;
use oap_sdk::{AgentEvent, ClientBuilder, ExecutionRequest};
use serde_json::json;

#[tokio::main(flavor = "current_thread")]
async fn main() {
    let args: Vec<String> = std::env::args().collect();
    let other = args[2] == "--non-codex";
    let arguments = if other {
        vec!["sdk/typescript/test/fixtures/oap-server.js"]
    } else {
        vec!["serve", "agent", "--backend", "codex", "--config", &args[2]]
    };
    let model = if other {
        "fixture/openai-responses@bash-no-exit"
    } else {
        "fixture/openai-responses@glm-test"
    };
    let client = ClientBuilder::new()
        .command(&args[1])
        .args(arguments)
        .connect()
        .await
        .expect("connect");
    let mut events = Box::pin(
        client
            .agent()
            .stream(ExecutionRequest::prompt(model, "read the code")),
    );
    let mut ends = Vec::new();
    while let Some(event) = events.next().await {
        if let AgentEvent::ToolExecutionEnd {
            tool_call_id,
            is_error,
            result,
        } = event.expect("event")
        {
            ends.push(
                json!({"tool_call_id": tool_call_id, "is_error": is_error, "result": result}),
            );
        }
    }
    client.close().await;
    println!("{}", json!(ends));
    if other {
        assert_eq!(ends.len(), 1);
        assert_eq!(
            ends[0]["is_error"], false,
            "non-Codex Bash without exitCode changed outcome"
        );
        return;
    }
    assert_eq!(ends.len(), 8);
    for (i, event) in ends.iter().enumerate() {
        assert_eq!(
            event["is_error"],
            json!(i != 0 && i != 5),
            "command outcome {i}"
        );
        if i < 6 {
            assert_eq!(
                event["result"]["exitCode"],
                json!([Some(0), Some(1), Some(-7), None, None, Some(0)][i]),
                "exit evidence {i}"
            );
            assert_eq!(event["result"]["command"], "git diff HEAD~1");
            assert_eq!(event["result"]["cwd"], "/workspace/repo");
        }
    }
}
