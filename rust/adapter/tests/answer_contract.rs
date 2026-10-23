use oap_adapter::{index_input_answers, validate_input_answer, Error};
use oap_protocol::{InputAnswer, InputOption, InputQuestion, InputQuestionKind};

fn question(kind: InputQuestionKind, required: bool) -> InputQuestion {
    InputQuestion {
        id: "q".into(),
        prompt: "Answer?".into(),
        required,
        options: if kind == InputQuestionKind::TEXT {
            Vec::new()
        } else {
            vec![InputOption {
                id: "yes".into(),
                label: "Yes".into(),
                ..InputOption::default()
            }]
        },
        kind,
    }
}

#[test]
fn optional_text_may_be_answered_with_empty_text() {
    let questions = [question(InputQuestionKind::TEXT, false)];
    let answers = [InputAnswer {
        question_id: "q".into(),
        ..InputAnswer::default()
    }];
    assert_eq!(validate_input_answer(&questions[0], &answers[0]), Ok(()));
    let indexed = index_input_answers(&questions, &answers).unwrap();
    assert_eq!(indexed["q"].text, "");
    assert!(index_input_answers(&questions, &[]).unwrap().is_empty());
}

#[test]
fn every_required_question_must_be_answered_and_a_valid_answer_repairs_the_set() {
    for kind in [
        InputQuestionKind::TEXT,
        InputQuestionKind::SINGLE_CHOICE,
        InputQuestionKind::MULTI_CHOICE,
    ] {
        let required = question(kind.clone(), true);
        let optional = InputQuestion {
            id: "optional".into(),
            ..question(InputQuestionKind::TEXT, false)
        };
        let questions = [required, optional];
        let unrelated = [InputAnswer {
            question_id: "optional".into(),
            text: "a note".into(),
            ..InputAnswer::default()
        }];
        for answers in [&[][..], &unrelated[..]] {
            assert_eq!(
                index_input_answers(&questions, answers).err(),
                Some(Error::InvalidResolution),
                "{kind}: missing required answer"
            );
        }
        let answers = [InputAnswer {
            question_id: "q".into(),
            text: if kind == InputQuestionKind::TEXT {
                "é漢😀".into()
            } else {
                String::new()
            },
            selected_option_ids: if kind == InputQuestionKind::TEXT {
                Vec::new()
            } else {
                vec!["yes".into()]
            },
        }];
        assert_eq!(index_input_answers(&questions, &answers).unwrap().len(), 1);
    }
}

#[test]
fn optional_text_does_not_accept_a_choice_or_mixed_answer() {
    let text = question(InputQuestionKind::TEXT, false);
    for value in ["", "text"] {
        let answer = InputAnswer {
            question_id: "q".into(),
            text: value.into(),
            selected_option_ids: vec!["yes".into()],
        };
        assert_eq!(
            validate_input_answer(&text, &answer),
            Err(Error::InvalidResolution)
        );
    }
}
