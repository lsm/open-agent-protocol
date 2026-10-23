use std::collections::{HashMap, HashSet};

use oap_protocol::{InputAnswer, InputQuestion};

use crate::Error;

pub fn validate_input_answer(question: &InputQuestion, answer: &InputAnswer) -> Result<(), Error> {
    let selected = &answer.selected_option_ids;
    let has_text = !answer.text.is_empty();
    let shaped = answer.question_id == question.id
        && !(has_text && !selected.is_empty())
        && match question.kind.as_str() {
            "text" => has_text || !question.required,
            "single_choice" => selected.len() == 1,
            "multi_choice" => !selected.is_empty(),
            _ => false,
        };
    if !shaped {
        return Err(Error::InvalidResolution);
    }
    let offered: HashSet<&str> = question
        .options
        .iter()
        .map(|option| option.id.as_str())
        .collect();
    let mut seen = HashSet::new();
    for id in selected {
        if id.is_empty() || !offered.contains(id.as_str()) || !seen.insert(id.as_str()) {
            return Err(Error::InvalidResolution);
        }
    }
    Ok(())
}

pub fn index_input_answers<'a>(
    questions: &'a [InputQuestion],
    answers: &'a [InputAnswer],
) -> Result<HashMap<&'a str, &'a InputAnswer>, Error> {
    let offered: HashMap<&str, &InputQuestion> = questions
        .iter()
        .map(|question| (question.id.as_str(), question))
        .collect();
    let mut indexed = HashMap::new();
    for answer in answers {
        let question = offered
            .get(answer.question_id.as_str())
            .ok_or(Error::InvalidResolution)?;
        if indexed.contains_key(answer.question_id.as_str()) {
            return Err(Error::InvalidResolution);
        }
        validate_input_answer(question, answer)?;
        indexed.insert(answer.question_id.as_str(), answer);
    }
    if questions
        .iter()
        .any(|question| question.required && !indexed.contains_key(question.id.as_str()))
    {
        return Err(Error::InvalidResolution);
    }
    Ok(indexed)
}
