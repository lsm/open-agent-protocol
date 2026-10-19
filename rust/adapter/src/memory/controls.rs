use oap_protocol::*;
use oap_validation::OutputSchema;

use super::{scripted_catalog, FIXED_RESULT, MODEL_PRIMARY, MODEL_SECONDARY};
use crate::{Error, UnsupportedControl, CONTROL_UNSATISFIABLE};

#[derive(Clone, Default)]
pub(super) struct Controls {
    pub(super) model: String,
    pub(super) instructions: String,
    pub(super) choice: Option<ToolChoice>,
    pub(super) output_schema: Option<String>,
    pub(super) elected: ToolDefinition,
    pub(super) calls_tool: bool,
}

fn unsatisfiable(feature: &str, detail: String) -> Error {
    let control = UnsupportedControl {
        feature: feature.to_owned(),
        reason: CONTROL_UNSATISFIABLE,
        detail,
        ..UnsupportedControl::default()
    };
    Error::UnsupportedControl(Box::new(control))
}

fn catalog(provided: &[ToolDefinition]) -> Vec<String> {
    callable(provided)
        .into_iter()
        .map(|tool| tool.name)
        .collect()
}

fn callable(provided: &[ToolDefinition]) -> Vec<ToolDefinition> {
    scripted_catalog()
        .into_iter()
        .chain(provided.iter().cloned())
        .collect()
}

fn elect(provided: &[ToolDefinition], policy: Option<&ToolChoice>) -> Option<ToolDefinition> {
    let candidates = if provided.is_empty() {
        callable(provided)
    } else {
        provided.to_vec()
    };
    let catalog = catalog(provided);
    candidates
        .into_iter()
        .find(|tool| policy.is_none_or(|policy| policy.permits(&tool.name, &catalog, true)))
}

pub(super) fn admit(
    provided: &[ToolDefinition],
    request: &MessageSubmitRequest,
) -> Result<Controls, Error> {
    let mut controls = Controls {
        calls_tool: true,
        ..Controls::default()
    };
    let mut refusals: Vec<(&str, Error)> = Vec::new();
    if let Some(instructions) = &request.instructions {
        controls.instructions = instructions.clone();
    }
    if let Some(model) = &request.model_id {
        if model != MODEL_PRIMARY && model != MODEL_SECONDARY {
            refusals.push((FEATURE_MODEL_SELECTION, Error::ModelNotFound(model.clone())));
        } else {
            controls.model = model.clone();
        }
    }
    match request.tool_choice_policy() {
        Err(err) => refusals.push((
            FEATURE_TOOL_SELECTION,
            unsatisfiable(FEATURE_TOOL_SELECTION, err.to_string()),
        )),
        Ok(Some(policy)) => match policy.unsatisfiable(&catalog(provided), true) {
            Some(defect) => {
                let control = UnsupportedControl {
                    feature: FEATURE_TOOL_SELECTION.to_owned(),
                    reason: CONTROL_UNSATISFIABLE,
                    tool: defect.tool,
                    detail: defect.reason.to_owned(),
                    ..UnsupportedControl::default()
                };
                refusals.push((
                    FEATURE_TOOL_SELECTION,
                    Error::UnsupportedControl(Box::new(control)),
                ));
            }
            None => controls.choice = Some(policy),
        },
        Ok(None) => {}
    }
    match elect(provided, controls.choice.as_ref()) {
        Some(tool) => controls.elected = tool,
        None => controls.calls_tool = false,
    }
    if let Some(schema) = &request.output_schema {
        let refused = |detail: String| {
            let control = UnsupportedControl {
                feature: FEATURE_STRUCTURED_OUTPUT.to_owned(),
                reason: CONTROL_UNSATISFIABLE,
                field: "output_schema".into(),
                detail,
                ..UnsupportedControl::default()
            };
            Error::UnsupportedControl(Box::new(control))
        };
        match OutputSchema::compile(schema.get()) {
            Err(err) => refusals.push((FEATURE_STRUCTURED_OUTPUT, refused(err))),
            Ok(compiled) if compiled.validate(FIXED_RESULT).is_err() => refusals.push((
                FEATURE_STRUCTURED_OUTPUT,
                refused("the fixed result does not satisfy the requested schema".into()),
            )),
            Ok(_) => controls.output_schema = Some(schema.get().to_owned()),
        }
    }
    refusals.sort_by(|a, b| a.0.cmp(b.0));
    match refusals.into_iter().next() {
        Some((_, err)) => Err(err),
        None => Ok(controls),
    }
}

#[cfg(test)]
mod tests {
    use oap_protocol::*;
    use serde_json::value::RawValue;

    use super::admit;
    use crate::{Error, CONTROL_UNSATISFIABLE};

    fn raw(text: &str) -> Option<Box<RawValue>> {
        (!text.is_empty()).then(|| RawValue::from_string(text.to_owned()).unwrap())
    }

    fn request(choice: &str, schema: &str) -> MessageSubmitRequest {
        MessageSubmitRequest {
            tool_choice: raw(choice),
            output_schema: raw(schema),
            ..MessageSubmitRequest::default()
        }
    }

    fn refused(result: Result<super::Controls, Error>) -> (String, &'static str, String) {
        match result {
            Err(Error::UnsupportedControl(control)) => {
                (control.feature, control.reason, control.tool)
            }
            Err(other) => panic!("{other:?}"),
            Ok(_) => panic!("admitted"),
        }
    }

    #[test]
    fn every_control_is_judged_unsatisfiable_under_its_own_key() {
        let cases = [
            (r#""none""#, "", FEATURE_TOOL_SELECTION, ""),
            (
                r#"{"allowed":["scripted_tool"],"limit":2}"#,
                "",
                FEATURE_TOOL_SELECTION,
                "",
            ),
            (
                r#"{"allowed":["scripted_tool"],"disallowed":["scripted_tool"]}"#,
                "",
                FEATURE_TOOL_SELECTION,
                "",
            ),
            (
                r#"{"allowed":["absent_tool"]}"#,
                "",
                FEATURE_TOOL_SELECTION,
                "absent_tool",
            ),
            ("", r#"{"type":"array"}"#, FEATURE_STRUCTURED_OUTPUT, ""),
            (
                "",
                r#"{"type":"object","properties":{"a":{"$ref":"https://example.test/s.json"}}}"#,
                FEATURE_STRUCTURED_OUTPUT,
                "",
            ),
            (
                "",
                r#"{"type":"object","required":"x"}"#,
                FEATURE_STRUCTURED_OUTPUT,
                "",
            ),
            (
                "",
                r#"{"type":"object","required":["answer"]}"#,
                FEATURE_STRUCTURED_OUTPUT,
                "",
            ),
        ];
        for (choice, schema, feature, tool) in cases {
            let (key, reason, named) = refused(admit(&[], &request(choice, schema)));
            assert_eq!(
                (key.as_str(), reason, named.as_str()),
                (feature, CONTROL_UNSATISFIABLE, tool),
                "{choice}{schema}"
            );
        }
    }

    #[test]
    fn the_lowest_capability_key_names_the_refusal() {
        let both = request(
            r#"{"allowed":["absent_tool"]}"#,
            r#"{"type":"object","required":"x"}"#,
        );
        assert_eq!(refused(admit(&[], &both)).0, FEATURE_STRUCTURED_OUTPUT);
        let with_model = MessageSubmitRequest {
            model_id: Some("no-such-model".into()),
            ..both
        };
        assert_eq!(
            admit(&[], &with_model).err(),
            Some(Error::ModelNotFound("no-such-model".into()))
        );
    }

    #[test]
    fn admitted_controls_carry_the_model_instructions_schema_and_elected_tool() {
        let full = MessageSubmitRequest {
            model_id: Some(super::MODEL_SECONDARY.into()),
            instructions: Some("Be terse.".into()),
            ..request(r#"{"allowed":["scripted_tool"]}"#, r#"{"type":"object"}"#)
        };
        let controls = admit(&[], &full).unwrap();
        assert_eq!(
            (controls.model.as_str(), controls.instructions.as_str()),
            (super::MODEL_SECONDARY, "Be terse.")
        );
        assert_eq!(
            controls.output_schema.as_deref(),
            Some(r#"{"type":"object"}"#)
        );
        assert!(controls.calls_tool && controls.elected.name == super::super::SCRIPTED_TOOL);
        for policy in [r#"{"disallowed":["scripted_tool"]}"#, r#"{"allowed":[]}"#] {
            assert!(
                !admit(&[], &request(policy, "")).unwrap().calls_tool,
                "{policy}"
            );
        }
    }

    #[test]
    fn a_provided_tool_is_elected_ahead_of_the_scripted_one() {
        let provided = [ToolDefinition {
            name: "lookup".into(),
            ..ToolDefinition::default()
        }];
        assert_eq!(
            admit(&provided, &request("", "")).unwrap().elected.name,
            "lookup"
        );
        let scripted_only = request(r#"{"allowed":["scripted_tool"]}"#, "");
        assert!(!admit(&provided, &scripted_only).unwrap().calls_tool);
    }
}
