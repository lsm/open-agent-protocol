package protocol

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestInputAnswerWire(t *testing.T) {
	for _, wire := range []string{
		`{"question_id":"q","text":""}`,
		`{"question_id":"q","text":"é漢😀"}`,
		`{"question_id":"q","selected_option_ids":["yes"]}`,
		`{"question_id":"q","text":"mixed","selected_option_ids":["yes"]}`,
	} {
		t.Run(wire, func(t *testing.T) {
			var answer InputAnswer
			if err := json.Unmarshal([]byte(wire), &answer); err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(answer)
			if err != nil {
				t.Fatal(err)
			}
			var expectedValue, actualValue any
			if err := json.Unmarshal([]byte(wire), &expectedValue); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(got, &actualValue); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(expectedValue, actualValue) {
				t.Fatalf("wire changed: want %s, got %s", wire, got)
			}
		})
	}
}

func TestEmptyInputTextSurvivesEveryPayload(t *testing.T) {
	answers := []InputAnswer{{QuestionID: "q"}}
	for _, entry := range []struct {
		payload any
		key     string
	}{
		{UserInputRequestedPayload{DraftAnswers: answers}, "draft_answers"},
		{UserInputResolveRequest{Answers: answers}, "answers"},
		{UserInputResolvedPayload{Answers: answers}, "answers"},
	} {
		data, err := json.Marshal(entry.payload)
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil {
			t.Fatal(err)
		}
		var decoded []map[string]any
		if err := json.Unmarshal(object[entry.key], &decoded); err != nil {
			t.Fatal(err)
		}
		if text, exists := decoded[0]["text"]; !exists || text != "" {
			t.Fatalf("empty text lost in %T: %s", entry.payload, data)
		}
		if _, exists := decoded[0]["selected_option_ids"]; exists {
			t.Fatalf("text answer gained choices: %s", data)
		}
	}
}
