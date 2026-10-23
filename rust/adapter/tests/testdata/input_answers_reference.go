package main

import (
	"encoding/json"
	"os"

	"github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func main() {
	var input struct {
		Questions []protocol.InputQuestion `json:"questions"`
		Answers   []protocol.InputAnswer   `json:"answers"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		panic(err)
	}
	valid := make([]bool, len(input.Answers))
	for i, answer := range input.Answers {
		for _, question := range input.Questions {
			if question.ID == answer.QuestionID {
				valid[i] = adapter.ValidateInputAnswer(question, answer) == nil
				break
			}
		}
	}
	_, err := adapter.IndexInputAnswers(input.Questions, input.Answers)
	if err := json.NewEncoder(os.Stdout).Encode(struct {
		Indexed bool                   `json:"indexed"`
		Valid   []bool                 `json:"valid"`
		Answers []protocol.InputAnswer `json:"answers"`
	}{err == nil, valid, input.Answers}); err != nil {
		panic(err)
	}
}
