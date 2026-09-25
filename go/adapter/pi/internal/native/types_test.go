package native

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestAllPinnedCommandsValidate(t *testing.T) {
	message, provider, model, path, entry, name, bash := "m", "p", "m", "/x", "e", "n", "ls"
	commands := []Command{
		{Type: CommandPrompt, Message: &message}, {Type: CommandSteer, Message: &message}, {Type: CommandFollowUp, Message: &message},
		{Type: CommandAbort}, {Type: CommandClearQueue}, {Type: CommandNewSession}, {Type: CommandGetState},
		{Type: CommandSetModel, Provider: &provider, ModelID: &model}, {Type: CommandCycleModel}, {Type: CommandGetAvailableModels},
		{Type: CommandSetThinkingLevel, Level: ThinkingMax}, {Type: CommandCycleThinkingLevel}, {Type: CommandGetAvailableThinkingLevels},
		{Type: CommandSetSteeringMode, Mode: QueueAll}, {Type: CommandSetFollowUpMode, Mode: QueueOneAtATime},
		{Type: CommandCompact}, {Type: CommandSetAutoCompaction, Enabled: Bool(false)}, {Type: CommandSetAutoRetry, Enabled: Bool(true)},
		{Type: CommandAbortRetry}, {Type: CommandBash, BashCommand: &bash}, {Type: CommandAbortBash}, {Type: CommandGetSessionStats},
		{Type: CommandExportHTML}, {Type: CommandSwitchSession, SessionPath: &path}, {Type: CommandFork, EntryID: &entry}, {Type: CommandClone},
		{Type: CommandGetForkMessages}, {Type: CommandGetEntries}, {Type: CommandGetTree}, {Type: CommandGetLastAssistantText},
		{Type: CommandSetSessionName, Name: &name}, {Type: CommandGetMessages}, {Type: CommandGetCommands},
	}
	for _, command := range commands {
		if err := command.Validate(); err != nil {
			t.Errorf("%s: %v", command.Type, err)
		}
	}
}

func TestCommandRejectsCrossVariantFields(t *testing.T) {
	message := "not valid"
	if err := (Command{Type: CommandAbort, Message: &message}).Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v", err)
	}
}

func TestCommandJSONMatchesPiFields(t *testing.T) {
	message := "hello"
	data, err := json.Marshal(Command{ID: "req_1", Type: CommandPrompt, Message: &message, StreamingBehavior: StreamingFollowUp})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), `{"id":"req_1","type":"prompt","message":"hello","streamingBehavior":"followUp"}`; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestExtensionRequestsValidatePinnedUnion(t *testing.T) {
	requests := []ExtensionUIRequest{
		{Type: "extension_ui_request", ID: "1", Method: ExtensionSelect, Title: "T", Options: []string{}},
		{Type: "extension_ui_request", ID: "2", Method: ExtensionConfirm, Title: "T", Message: "M"},
		{Type: "extension_ui_request", ID: "3", Method: ExtensionInput, Title: "T"},
		{Type: "extension_ui_request", ID: "4", Method: ExtensionEditor, Title: "T"},
		{Type: "extension_ui_request", ID: "5", Method: ExtensionNotify, Message: "M", NotifyType: "warning"},
		{Type: "extension_ui_request", ID: "6", Method: ExtensionSetStatus, StatusKey: "k"},
		{Type: "extension_ui_request", ID: "7", Method: ExtensionSetWidget, WidgetKey: "k", WidgetPlacement: "aboveEditor"},
		{Type: "extension_ui_request", ID: "8", Method: ExtensionSetTitle, Title: "T"},
		{Type: "extension_ui_request", ID: "9", Method: ExtensionSetEditorText},
	}
	for _, request := range requests {
		if err := request.Validate(); err != nil {
			t.Errorf("%s: %v", request.Method, err)
		}
	}
	invalid := []ExtensionUIRequest{
		{Type: "extension_ui_request", ID: "x", Method: ExtensionSelect},
		{Type: "extension_ui_request", ID: "x", Method: ExtensionNotify, Message: "M", NotifyType: "bogus"},
		{Type: "extension_ui_request", ID: "x", Method: ExtensionSetWidget, WidgetKey: "k", WidgetPlacement: "side"},
		{Type: "extension_ui_request", ID: "x", Method: ExtensionConfirm, Title: "T", Message: "M", WidgetKey: "foreign"},
	}
	for _, request := range invalid {
		if err := request.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted %+v: %v", request, err)
		}
	}
}

func TestExtensionResponseRequiresOneOutcome(t *testing.T) {
	for _, response := range []ExtensionUIResponse{{Type: "extension_ui_response", ID: "x"}, {Type: "extension_ui_response", ID: "x", Value: String("x"), Cancelled: true}} {
		if err := response.Validate(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", response, err)
		}
	}
	if err := (ExtensionUIResponse{Type: "extension_ui_response", ID: "x", Confirmed: Bool(false)}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeStrictRejectsUnknownAndDuplicate(t *testing.T) {
	var response Response
	if err := DecodeStrict([]byte(`{"type":"response","type":"response","command":"prompt","success":true}`), &response); err == nil {
		t.Fatal("accepted duplicate")
	}
	if err := DecodeStrict([]byte(`{"type":"response","command":"prompt","success":true,"extra":1}`), &response); err == nil {
		t.Fatal("accepted unknown")
	}
}
