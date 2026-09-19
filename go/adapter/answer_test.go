package adapter

import (
	"errors"
	"testing"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

func TestValidateInputAnswer(t *testing.T) {
	text := protocol.InputQuestion{ID: "note", Kind: protocol.InputText}
	single := protocol.InputQuestion{ID: "pick", Kind: protocol.InputSingleChoice, Options: []protocol.InputOption{{ID: "a"}, {ID: "b"}}}
	multi := protocol.InputQuestion{ID: "many", Kind: protocol.InputMultiChoice, Options: []protocol.InputOption{{ID: "x"}, {ID: "y"}}}
	cases := []struct {
		name     string
		question protocol.InputQuestion
		answer   protocol.InputAnswer
		valid    bool
	}{
		{"text ok", text, protocol.InputAnswer{QuestionID: "note", Text: "hello"}, true},
		{"text empty", text, protocol.InputAnswer{QuestionID: "note"}, false},
		{"text with selections", text, protocol.InputAnswer{QuestionID: "note", Text: "hello", SelectedOptionIDs: []string{"a"}}, false},
		{"text foreign question", text, protocol.InputAnswer{QuestionID: "other", Text: "hello"}, false},

		{"single ok", single, protocol.InputAnswer{QuestionID: "pick", SelectedOptionIDs: []string{"a"}}, true},
		{"single none", single, protocol.InputAnswer{QuestionID: "pick"}, false},
		{"single two", single, protocol.InputAnswer{QuestionID: "pick", SelectedOptionIDs: []string{"a", "b"}}, false},
		{"single unoffered", single, protocol.InputAnswer{QuestionID: "pick", SelectedOptionIDs: []string{"c"}}, false},
		{"single empty id", single, protocol.InputAnswer{QuestionID: "pick", SelectedOptionIDs: []string{""}}, false},
		{"single with text", single, protocol.InputAnswer{QuestionID: "pick", SelectedOptionIDs: []string{"a"}, Text: "a"}, false},
		{"single text only", single, protocol.InputAnswer{QuestionID: "pick", Text: "a"}, false},

		{"multi ok one", multi, protocol.InputAnswer{QuestionID: "many", SelectedOptionIDs: []string{"x"}}, true},
		{"multi ok two", multi, protocol.InputAnswer{QuestionID: "many", SelectedOptionIDs: []string{"x", "y"}}, true},
		{"multi none", multi, protocol.InputAnswer{QuestionID: "many"}, false},
		{"multi duplicate", multi, protocol.InputAnswer{QuestionID: "many", SelectedOptionIDs: []string{"x", "x"}}, false},
		{"multi unoffered", multi, protocol.InputAnswer{QuestionID: "many", SelectedOptionIDs: []string{"x", "z"}}, false},
		{"multi with text", multi, protocol.InputAnswer{QuestionID: "many", SelectedOptionIDs: []string{"x"}, Text: "x"}, false},

		{"unknown kind", protocol.InputQuestion{ID: "q", Kind: "bogus"}, protocol.InputAnswer{QuestionID: "q", Text: "x"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateInputAnswer(tc.question, tc.answer)
			if tc.valid && err != nil {
				t.Fatalf("want valid, got %v", err)
			}
			if !tc.valid && !errors.Is(err, ErrInvalidResolution) {
				t.Fatalf("want ErrInvalidResolution, got %v", err)
			}
		})
	}
}

func TestIndexInputAnswers(t *testing.T) {
	questions := []protocol.InputQuestion{
		{ID: "pick", Kind: protocol.InputSingleChoice, Options: []protocol.InputOption{{ID: "a"}}},
		{ID: "note", Kind: protocol.InputText},
	}
	if _, err := IndexInputAnswers(questions, []protocol.InputAnswer{{QuestionID: "pick", SelectedOptionIDs: []string{"a"}}, {QuestionID: "note", Text: "hi"}}); err != nil {
		t.Fatalf("valid set rejected: %v", err)
	}
	for name, answers := range map[string][]protocol.InputAnswer{
		"duplicate answer": {{QuestionID: "note", Text: "a"}, {QuestionID: "note", Text: "b"}},
		"foreign question": {{QuestionID: "other", Text: "a"}},
		"malformed answer": {{QuestionID: "pick", SelectedOptionIDs: []string{"a"}, Text: "a"}},
		"unoffered option": {{QuestionID: "pick", SelectedOptionIDs: []string{"z"}}},
	} {
		if _, err := IndexInputAnswers(questions, answers); !errors.Is(err, ErrInvalidResolution) {
			t.Fatalf("%s: got %v, want ErrInvalidResolution", name, err)
		}
	}
}
