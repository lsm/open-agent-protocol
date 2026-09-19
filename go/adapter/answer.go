package adapter

import "github.com/lsm/open-agent-protocol/go/protocol"

func ValidateInputAnswer(question protocol.InputQuestion, answer protocol.InputAnswer) error {
	if answer.QuestionID != question.ID {
		return ErrInvalidResolution
	}
	hasText := answer.Text != ""
	if hasText && len(answer.SelectedOptionIDs) != 0 {
		return ErrInvalidResolution
	}
	switch question.Kind {
	case protocol.InputText:
		if !hasText {
			return ErrInvalidResolution
		}
	case protocol.InputSingleChoice:
		if len(answer.SelectedOptionIDs) != 1 {
			return ErrInvalidResolution
		}
	case protocol.InputMultiChoice:
		if len(answer.SelectedOptionIDs) == 0 {
			return ErrInvalidResolution
		}
	default:
		return ErrInvalidResolution
	}
	offered := make(map[string]bool, len(question.Options))
	for _, option := range question.Options {
		offered[option.ID] = true
	}
	seen := make(map[string]bool, len(answer.SelectedOptionIDs))
	for _, id := range answer.SelectedOptionIDs {
		if id == "" || !offered[id] || seen[id] {
			return ErrInvalidResolution
		}
		seen[id] = true
	}
	return nil
}

func IndexInputAnswers(questions []protocol.InputQuestion, answers []protocol.InputAnswer) (map[string]protocol.InputAnswer, error) {
	offered := make(map[string]protocol.InputQuestion, len(questions))
	for _, question := range questions {
		offered[question.ID] = question
	}
	indexed := make(map[string]protocol.InputAnswer, len(answers))
	for _, answer := range answers {
		question, ok := offered[answer.QuestionID]
		if !ok {
			return nil, ErrInvalidResolution
		}
		if _, duplicate := indexed[answer.QuestionID]; duplicate {
			return nil, ErrInvalidResolution
		}
		if err := ValidateInputAnswer(question, answer); err != nil {
			return nil, err
		}
		indexed[answer.QuestionID] = answer
	}
	return indexed, nil
}
