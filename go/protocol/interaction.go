package protocol

type InputQuestionKind string

const (
	InputText         InputQuestionKind = "text"
	InputSingleChoice InputQuestionKind = "single_choice"
	InputMultiChoice  InputQuestionKind = "multi_choice"
)

type InputOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type InputQuestion struct {
	ID       string            `json:"id"`
	Prompt   string            `json:"prompt"`
	Kind     InputQuestionKind `json:"kind"`
	Required bool              `json:"required,omitempty"`
	Options  []InputOption     `json:"options,omitempty"`
}

type InputAnswer struct {
	QuestionID        string   `json:"question_id"`
	Text              string   `json:"text,omitempty"`
	SelectedOptionIDs []string `json:"selected_option_ids,omitempty"`
}

type UserInputRequestedPayload struct {
	InteractionID InteractionID   `json:"interaction_id"`
	RequestedBy   ParticipantID   `json:"requested_by"`
	RespondedBy   ParticipantID   `json:"responded_by"`
	SessionID     SessionID       `json:"session_id"`
	RunID         RunID           `json:"run_id"`
	ToolCallID    ToolCallID      `json:"tool_call_id,omitempty"`
	Title         string          `json:"title"`
	Description   string          `json:"description,omitempty"`
	Questions     []InputQuestion `json:"questions"`
	AllowCancel   bool            `json:"allow_cancel,omitempty"`
	DraftAnswers  []InputAnswer   `json:"draft_answers,omitempty"`
}

type UserInputResolveRequest struct {
	InteractionID InteractionID `json:"interaction_id"`
	RequestedBy   ParticipantID `json:"requested_by"`
	RespondedBy   ParticipantID `json:"responded_by"`
	SessionID     SessionID     `json:"session_id"`
	RunID         RunID         `json:"run_id"`
	Answers       []InputAnswer `json:"answers"`
}

type UserInputResolutionStatus string

const (
	InputSubmitted UserInputResolutionStatus = "submitted"
	InputCancelled UserInputResolutionStatus = "cancelled"
)

type UserInputResolvedPayload struct {
	InteractionID InteractionID             `json:"interaction_id"`
	RequestedBy   ParticipantID             `json:"requested_by"`
	RespondedBy   ParticipantID             `json:"responded_by"`
	SessionID     SessionID                 `json:"session_id"`
	RunID         RunID                     `json:"run_id"`
	Status        UserInputResolutionStatus `json:"status"`
	Answers       []InputAnswer             `json:"answers,omitempty"`
}

type UserInputResolveResponse struct {
	InteractionID InteractionID `json:"interaction_id"`
	SessionID     SessionID     `json:"session_id"`
	RunID         RunID         `json:"run_id"`
	Accepted      bool          `json:"accepted"`
}

type UserInputCancelRequest struct {
	InteractionID InteractionID `json:"interaction_id"`
	RequestedBy   ParticipantID `json:"requested_by"`
	RespondedBy   ParticipantID `json:"responded_by"`
	SessionID     SessionID     `json:"session_id"`
	RunID         RunID         `json:"run_id"`
	Reason        string        `json:"reason,omitempty"`
}

type UserInputCancelResponse = UserInputResolveResponse
