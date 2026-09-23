use std::collections::BTreeMap;
use std::sync::{Arc, Mutex};

use futures::StreamExt;
use oap_sdk::{
    AuthEvent, AuthHandlers, AuthRetryPolicy, AuthStatus, ChatMessage, ClientBuilder, Content,
    ContentPart, Error, ExecutionRequest, ListModelsRequest, Role, Tool,
};
use serde_json::{json, Value};

#[tokio::test]
async fn combined_oap_connection_runs_both_profiles() {
    let client = ClientBuilder::new()
        .command(env!("CARGO_BIN_EXE_oap-protocol-fake"))
        .args([] as [&str; 0])
        .connect()
        .await
        .expect("connects to OAP profiles");
    let listed = client
        .models()
        .list(ListModelsRequest::default())
        .await
        .expect("models");
    assert_eq!(listed.models[0].model_ref, "fixture/openai-responses@mock");
    let request = ExecutionRequest::prompt("fixture/openai-responses@mock", "hello");
    let provider = client
        .provider()
        .complete(request.clone())
        .await
        .expect("inference");
    assert_eq!(provider.text(), "provider works");
    assert_eq!(provider.usage.expect("usage").output, 2);
    let agent = client.agent().run(request).await.expect("agent run");
    assert_eq!(agent.text(), "agent works");
    assert_eq!(agent.usage.expect("usage").output, 3);
    client
        .agent()
        .switch_model("existing-session", "fixture/openai-responses@switched")
        .await
        .expect("switch model");
    let selected = client
        .agent()
        .run_selected(
            "existing-session",
            vec![oap_sdk::ChatMessage::user("hello again")],
        )
        .await
        .expect("run with selected model");
    assert_eq!(selected.text(), "agent works");
    assert_eq!(selected.model_id, "switched");
    let mut streamed = Box::pin(
        client
            .agent()
            .stream_selected("existing-session", vec![ChatMessage::user("hello again")]),
    );
    let mut text = String::new();
    while let Some(event) = streamed.next().await {
        if let oap_sdk::AgentEvent::Provider(oap_sdk::ProviderEvent::TextDelta { delta }) =
            event.expect("selected stream event")
        {
            text.push_str(&delta);
        }
    }
    assert_eq!(text, "agent works");
    client.close().await;
}

#[tokio::test]
async fn oap_auth_discovery_progress_and_completion() {
    let client = ClientBuilder::new()
        .command(env!("CARGO_BIN_EXE_oap-protocol-fake"))
        .args([] as [&str; 0])
        .connect()
        .await
        .expect("connects");
    let providers = client.auth().list_providers().await.expect("providers");
    assert_eq!(providers[0].id, "fixture");
    let seen = Arc::new(Mutex::new(Vec::new()));
    let seen_events = Arc::clone(&seen);
    let handlers = AuthHandlers::new()
        .on_event(move |event| seen_events.lock().expect("lock").push(event))
        .on_prompt(|_| async move { panic!("unexpected OAP prompt") });
    client
        .auth()
        .login("fixture", Some(&handlers))
        .await
        .expect("login");
    assert!(matches!(
        seen.lock().expect("lock").last(),
        Some(AuthEvent::Success { .. })
    ));
    assert_eq!(
        client.auth().list_providers().await.expect("providers")[0].auth_status,
        AuthStatus::Authenticated
    );
    client.close().await;
}

#[tokio::test]
async fn oap_auto_once_retries_provider_and_agent_after_auth_rejection() {
    for agent in [false, true] {
        let client = ClientBuilder::new()
            .command(env!("CARGO_BIN_EXE_oap-protocol-fake"))
            .args([] as [&str; 0])
            .auth_retry_policy(AuthRetryPolicy::AutoOnce)
            .auth_handlers(AuthHandlers::new().on_prompt(|_| async { Ok("code".to_owned()) }))
            .connect()
            .await
            .expect("connects");
        let request = ExecutionRequest::prompt("fixture/openai-responses@needs-login", "hello");
        let result = if agent {
            client.agent().run(request).await
        } else {
            client.provider().complete(request).await
        };
        assert_eq!(
            result.expect("retried").text(),
            if agent {
                "agent works"
            } else {
                "provider works"
            }
        );
        client.close().await;
    }
}

#[tokio::test]
async fn oap_browser_login_needs_no_prompt_handler() {
    let client = ClientBuilder::new()
        .command(env!("CARGO_BIN_EXE_oap-protocol-fake"))
        .args([] as [&str; 0])
        .connect()
        .await
        .expect("connects");
    client.auth().login("fixture", None).await.expect("login");
    client.close().await;
}

#[tokio::test]
async fn oap_manual_prompt_never_calls_answer_handler() {
    let client = ClientBuilder::new()
        .command(env!("CARGO_BIN_EXE_oap-protocol-fake"))
        .args([] as [&str; 0])
        .connect()
        .await
        .expect("connects");
    let handlers = AuthHandlers::new()
        .on_prompt(|_| async move { panic!("OAP must not call the answer handler") });
    let error = client
        .auth()
        .login("manual", Some(&handlers))
        .await
        .expect_err("manual prompt fails");
    assert!(matches!(error, Error::Auth { .. }));
    client.close().await;
}

#[tokio::test]
async fn oap_auto_once_retries_streams_before_content() {
    for agent in [false, true] {
        let client = ClientBuilder::new()
            .command(env!("CARGO_BIN_EXE_oap-protocol-fake"))
            .args([] as [&str; 0])
            .auth_retry_policy(AuthRetryPolicy::AutoOnce)
            .auth_handlers(AuthHandlers::new().on_prompt(|_| async { Ok("code".to_owned()) }))
            .connect()
            .await
            .expect("connects");
        let request = ExecutionRequest::prompt("fixture/openai-responses@needs-login", "hello");
        let mut text = String::new();
        if agent {
            let mut events = Box::pin(client.agent().stream(request));
            while let Some(event) = events.next().await {
                if let oap_sdk::AgentEvent::Provider(oap_sdk::ProviderEvent::TextDelta { delta }) =
                    event.expect("event")
                {
                    text.push_str(&delta);
                }
            }
        } else {
            let mut events = Box::pin(client.provider().stream(request));
            while let Some(event) = events.next().await {
                if let oap_sdk::ProviderEvent::TextDelta { delta } = event.expect("event") {
                    text.push_str(&delta);
                }
            }
        }
        assert_eq!(
            text,
            if agent {
                "agent works"
            } else {
                "provider works"
            }
        );
        client.close().await;
    }
}

#[tokio::test]
async fn unrepresented_tools_and_agent_sampling_fail_explicitly() {
    let client = ClientBuilder::new()
        .command(env!("CARGO_BIN_EXE_oap-protocol-fake"))
        .args([] as [&str; 0])
        .connect()
        .await
        .expect("connects");
    let tool = Tool::new("local", "local", "{}").on_call(|_| async { Ok("ok".to_owned()) });
    let provider =
        ExecutionRequest::prompt("fixture/openai-responses@mock", "hello").with_tool(tool);
    let error = client
        .provider()
        .complete(provider)
        .await
        .expect_err("callback unsupported");
    assert!(
        matches!(error, Error::Protocol { code: Some(code), .. } if code == "unsupported_feature")
    );
    let agent = ExecutionRequest::prompt("fixture/openai-responses@mock", "hello")
        .with_tool(Tool::new("local", "local", "{}"));
    let error = client
        .agent()
        .run(agent)
        .await
        .expect_err("agent tools unsupported");
    assert!(
        matches!(error, Error::Protocol { code: Some(code), .. } if code == "unsupported_feature")
    );
    let agent =
        ExecutionRequest::prompt("fixture/openai-responses@mock", "hello").with_max_tokens(32);
    let error = client
        .agent()
        .run(agent)
        .await
        .expect_err("agent sampling unsupported");
    assert!(
        matches!(error, Error::Protocol { code: Some(code), .. } if code == "unsupported_feature")
    );
    client.close().await;
}

#[tokio::test]
async fn direct_completion_preserves_structured_parts_and_stream_hides_partial_tool_json() {
    let client = ClientBuilder::new()
        .command(env!("CARGO_BIN_EXE_oap-protocol-fake"))
        .args([] as [&str; 0])
        .connect()
        .await
        .expect("connects");
    let request = ExecutionRequest::prompt("fixture/openai-responses@structured", "hello");
    let response = client
        .provider()
        .complete(request.clone())
        .await
        .expect("structured response");
    assert_eq!(
        response.content,
        Content::Parts(vec![
            ContentPart::Thinking {
                thinking: "thinking".into(),
                thinking_signature: Some("reasoning-carry".into())
            },
            ContentPart::ToolCall {
                tool_call_id: "call-1".into(),
                name: "lookup".into(),
                arguments_json: r#"{"city":"Paris"}"#.into(),
                carry: Some("tool-carry".into())
            },
            ContentPart::ToolResult {
                tool_call_id: "call-0".into(),
                tool_name: String::new(),
                content: "prior result".into(),
                is_error: Some(false)
            },
            ContentPart::Image {
                data: "aGVsbG8=".into(),
                mime_type: "image/png".into()
            },
            ContentPart::text("answer"),
        ])
    );
    let mut stream = Box::pin(client.provider().stream(request));
    let mut text = Vec::new();
    let mut calls = Vec::new();
    while let Some(event) = stream.next().await {
        match event.expect("stream event") {
            oap_sdk::ProviderEvent::TextDelta { delta } => text.push(delta),
            oap_sdk::ProviderEvent::ToolCall { arguments_json, .. } => calls.push(arguments_json),
            _ => {}
        }
    }
    assert_eq!(text, vec!["provider works"]);
    assert_eq!(calls, vec![r#"{"city":"Paris"}"#]);
    client.close().await;
}

#[tokio::test]
async fn direct_request_uses_oap_schema_metadata_and_correlated_tool_result() {
    let client = ClientBuilder::new()
        .command(env!("CARGO_BIN_EXE_oap-protocol-fake"))
        .args([] as [&str; 0])
        .connect()
        .await
        .expect("connects");
    let assistant = ChatMessage::assistant(Content::Parts(vec![ContentPart::ToolCall {
        tool_call_id: "call-1".into(),
        name: "lookup".into(),
        arguments_json: r#"{"city":"Paris"}"#.into(),
        carry: Some("tool-carry".into()),
    }]));
    let mut tool_result = ChatMessage::new(Role::Tool, "sunny");
    tool_result.tool_call_id = Some("call-1".into());
    let mut request = ExecutionRequest::new(
        "fixture/openai-responses@echo",
        vec![assistant, tool_result],
    )
    .with_tool(Tool::new(
        "lookup",
        "City lookup",
        r#"{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}"#,
    ));
    request.options.metadata = Some(BTreeMap::from([("trace".into(), "test".into())]));
    let response = client
        .provider()
        .complete(request)
        .await
        .expect("echo response");
    let sent: Value = serde_json::from_str(&response.text()).expect("JSON echo");
    assert_eq!(
        sent.get("tools"),
        Some(
            &json!([{ "name": "lookup", "description": "City lookup", "input_schema": { "type": "object", "properties": { "city": { "type": "string" } }, "required": ["city"] } }])
        )
    );
    assert_eq!(sent.get("metadata"), Some(&json!({ "trace": "test" })));
    assert!(sent.get("metadata_json").is_none());
    assert_eq!(
        sent.get("messages")
            .and_then(Value::as_array)
            .and_then(|messages| messages.first())
            .and_then(|message| message.get("content")),
        Some(
            &json!([{ "type": "tool_call", "tool_call_id": "call-1", "name": "lookup", "arguments_json": { "city": "Paris" }, "carry": "tool-carry" }])
        )
    );
    assert_eq!(
        sent.get("messages")
            .and_then(Value::as_array)
            .and_then(|messages| messages.get(1)),
        Some(
            &json!({ "role": "tool", "content": [{ "type": "tool_result", "tool_call_id": "call-1", "result": "sunny" }] })
        )
    );
    client.close().await;
}

#[tokio::test]
async fn credential_rejected_becomes_typed_auth_required() {
    let client = ClientBuilder::new()
        .command(env!("CARGO_BIN_EXE_oap-protocol-fake"))
        .args([] as [&str; 0])
        .connect()
        .await
        .expect("connects");
    let error = client
        .provider()
        .complete(ExecutionRequest::prompt(
            "fixture/openai-responses@credential-rejected",
            "hello",
        ))
        .await
        .expect_err("credential is rejected");
    assert!(matches!(error, Error::AuthRequired { provider_id, .. } if provider_id == "fixture"));
    client.close().await;
}
