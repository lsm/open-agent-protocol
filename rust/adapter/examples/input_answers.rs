use std::io::{self, Read};

use oap_adapter::{index_input_answers, validate_input_answer};
use oap_protocol::{InputAnswer, InputQuestion};
use serde::Deserialize;
use serde_json::json;

#[derive(Deserialize)]
struct Input {
    questions: Vec<InputQuestion>,
    answers: Vec<InputAnswer>,
}

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let mut source = String::new();
    io::stdin().read_to_string(&mut source)?;
    let input: Input = serde_json::from_str(&source)?;
    let valid: Vec<bool> = input
        .answers
        .iter()
        .map(|answer| {
            input
                .questions
                .iter()
                .find(|question| question.id == answer.question_id)
                .is_some_and(|question| validate_input_answer(question, answer).is_ok())
        })
        .collect();
    println!(
        "{}",
        json!({
            "indexed": index_input_answers(&input.questions, &input.answers).is_ok(),
            "valid": valid,
            "answers": input.answers,
        })
    );
    Ok(())
}
