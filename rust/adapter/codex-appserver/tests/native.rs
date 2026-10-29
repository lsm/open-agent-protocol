use std::collections::BTreeMap;
use std::path::Path;

use oap_adapter_codex_appserver::native::*;
use serde::de::DeserializeOwned;
use serde_json::value::RawValue;
use serde_json::Value;

fn corpus_frames() -> Vec<Value> {
    let cases = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../../fixtures/adapters/codex-appserver-0.163.0/cases");
    let mut frames = Vec::new();
    for case in std::fs::read_dir(cases).unwrap() {
        let native = std::fs::read_to_string(case.unwrap().path().join("native.jsonl")).unwrap();
        frames.extend(
            native
                .lines()
                .filter(|line| !line.trim().is_empty())
                .map(|line| serde_json::from_str::<Value>(line).unwrap()),
        );
    }
    frames
}

fn decode<T: DeserializeOwned>(params: &Value) -> T {
    serde_json::from_str(&params.to_string()).unwrap()
}

fn text(params: &Value, key: &str) -> String {
    params[key].as_str().unwrap_or_default().to_owned()
}

#[test]
fn every_server_frame_in_the_corpus_but_the_declined_permissions_request_decodes_into_its_native_type_with_its_ids(
) {
    let mut decoded = BTreeMap::<String, usize>::new();
    let mut declined = Vec::new();
    for frame in corpus_frames() {
        if frame["direction"] != "server_to_client" {
            continue;
        }
        let method = frame["method"].as_str().unwrap().to_owned();
        let params = &frame["params"];
        let thread = text(params, "threadId");
        match method.as_str() {
            METHOD_TURN_STARTED | METHOD_TURN_COMPLETED => {
                let value: TurnNotification = decode(params);
                assert_eq!(value.thread_id, thread);
                assert_eq!(value.turn.id, params["turn"]["id"].as_str().unwrap());
                assert_eq!(
                    value.turn.status,
                    params["turn"]["status"].as_str().unwrap()
                );
            }
            METHOD_ITEM_STARTED | METHOD_ITEM_COMPLETED => {
                let value: ItemNotification = decode(params);
                assert_eq!(
                    (value.thread_id, value.turn_id),
                    (thread, text(params, "turnId"))
                );
                assert_eq!(value.item.id, params["item"]["id"].as_str().unwrap());
                assert_eq!(value.item.kind, params["item"]["type"].as_str().unwrap());
            }
            METHOD_AGENT_DELTA => {
                let value: AgentMessageDeltaNotification = decode(params);
                assert_eq!(value.thread_id, thread);
                assert_eq!(value.delta, text(params, "delta"));
            }
            METHOD_TOKEN_USAGE => {
                let value: ThreadTokenUsageUpdatedNotification = decode(params);
                let total = &params["tokenUsage"]["total"];
                assert_eq!(
                    value.token_usage.total.total_tokens,
                    total["totalTokens"].as_i64().unwrap()
                );
                assert_eq!(
                    value.token_usage.total.output_tokens,
                    total["outputTokens"].as_i64().unwrap()
                );
            }
            METHOD_COMMAND_APPROVAL => {
                let value: CommandApprovalParams = decode(params);
                assert_eq!(value.item_id, text(params, "itemId"));
            }
            METHOD_FILE_APPROVAL => {
                let value: FileApprovalParams = decode(params);
                assert_eq!(value.item_id, text(params, "itemId"));
            }
            METHOD_USER_INPUT => {
                let value: UserInputRequestParams = decode(params);
                let questions = params["questions"].as_array().unwrap();
                assert_eq!(value.questions.len(), questions.len());
                assert_eq!(value.questions[0].id, questions[0]["id"].as_str().unwrap());
            }
            _ => {
                declined.push(method);
                continue;
            }
        }
        *decoded.entry(method).or_default() += 1;
    }
    assert_eq!(declined, ["item/permissions/requestApproval"]);
    assert_eq!(
        decoded.keys().map(String::as_str).collect::<Vec<_>>(),
        [
            METHOD_AGENT_DELTA,
            METHOD_COMMAND_APPROVAL,
            METHOD_ITEM_COMPLETED,
            METHOD_FILE_APPROVAL,
            METHOD_ITEM_STARTED,
            METHOD_USER_INPUT,
            METHOD_TOKEN_USAGE,
            METHOD_TURN_COMPLETED,
            METHOD_TURN_STARTED,
        ]
    );
}

#[test]
fn a_null_element_in_an_item_s_changes_or_a_question_s_options_is_its_zero_value() {
    let item: ItemNotification =
        serde_json::from_str(r#"{"item":{"type":"fileChange","changes":[null,{"path":"a"}]}}"#)
            .unwrap();
    let changes = item.item.changes.unwrap();
    assert_eq!(
        changes
            .iter()
            .map(|change| change.path.as_str())
            .collect::<Vec<_>>(),
        ["", "a"]
    );
    let request: UserInputRequestParams =
        serde_json::from_str(r#"{"questions":[{"id":"q","options":[null,{"label":"Yes"}]}]}"#)
            .unwrap();
    let options = request.questions[0].options.as_ref().unwrap();
    assert_eq!(
        options
            .iter()
            .map(|option| option.label.as_str())
            .collect::<Vec<_>>(),
        ["", "Yes"]
    );
    let bare: UserInputRequestParams =
        serde_json::from_str(r#"{"questions":[{"id":"q","options":null}]}"#).unwrap();
    assert!(bare.questions[0].options.is_none());
}

#[test]
fn a_null_member_decodes_as_go_s_zero_value_and_an_array_for_a_struct_is_refused() {
    let value: TurnNotification = serde_json::from_str(r#"{"threadId":null,"turn":null}"#).unwrap();
    assert_eq!((value.thread_id.as_str(), value.turn.id.as_str()), ("", ""));
    let usage: ThreadTokenUsageUpdatedNotification =
        serde_json::from_str(r#"{"tokenUsage":{"total":{"totalTokens":null}}}"#).unwrap();
    assert_eq!(usage.token_usage.total, TokenUsageBreakdown::default());
    assert!(serde_json::from_str::<TurnNotification>(r#"{"turn":[1]}"#).is_err());
}

#[test]
fn requests_marshal_with_go_s_member_names_and_omit_what_go_omits() {
    assert_eq!(
        serde_json::to_string(&ThreadStartParams::default()).unwrap(),
        "{}"
    );
    let start = ThreadStartParams {
        model: "gpt".into(),
        cwd: "/w".into(),
        approval_policy: "on-request".into(),
        sandbox: "workspace-write".into(),
        config: BTreeMap::from([("k".into(), Value::Bool(true))]),
    };
    assert_eq!(
        serde_json::to_string(&start).unwrap(),
        r#"{"model":"gpt","cwd":"/w","approvalPolicy":"on-request","sandbox":"workspace-write","config":{"k":true}}"#
    );
    assert_eq!(
        serde_json::to_string(&ThreadResumeParams {
            thread_id: "t".into(),
            ..ThreadResumeParams::default()
        })
        .unwrap(),
        r#"{"threadId":"t"}"#
    );
    let turn = TurnStartParams {
        thread_id: "t".into(),
        input: vec![UserInput {
            kind: "text".into(),
            text: "hi".into(),
        }],
        effort: "high".into(),
        output_schema: Some(RawValue::from_string(r#"{"type":"object"}"#.into()).unwrap()),
        ..TurnStartParams::default()
    };
    assert_eq!(
        serde_json::to_string(&turn).unwrap(),
        r#"{"threadId":"t","input":[{"type":"text","text":"hi"}],"effort":"high","outputSchema":{"type":"object"}}"#
    );
    assert_eq!(
        serde_json::to_string(&TurnInterruptParams {
            thread_id: "t".into(),
            turn_id: "u".into(),
        })
        .unwrap(),
        r#"{"threadId":"t","turnId":"u"}"#
    );
    let answer = UserInputResponse {
        answers: BTreeMap::from([(
            "q".into(),
            UserInputAnswer {
                answers: vec!["yes".into()],
            },
        )]),
    };
    assert_eq!(
        serde_json::to_string(&answer).unwrap(),
        r#"{"answers":{"q":{"answers":["yes"]}}}"#
    );
    assert_eq!(
        serde_json::to_string(&ApprovalResponse {
            decision: "accept".into(),
        })
        .unwrap(),
        r#"{"decision":"accept"}"#
    );
}
