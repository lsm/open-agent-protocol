package protocol

import (
	"encoding/json"
	"testing"
)

func TestResolveRequestArm(t *testing.T) {
	cases := map[string]struct {
		request ActionCallResolveRequest
		want    string
	}{
		"acknowledgement": {ActionCallResolveRequest{Started: &ResolveArmStarted{}}, ResolveArmAcknowledge},
		"result":          {ActionCallResolveRequest{Result: json.RawMessage(`{"ok":true}`)}, ResolveArmResult},
		"null result":     {ActionCallResolveRequest{Result: json.RawMessage(`null`)}, ResolveArmResult},
		"error":           {ActionCallResolveRequest{Error: &ProtocolError{Code: "x", Message: "boom"}}, ResolveArmError},
		"none":            {ActionCallResolveRequest{}, ""},
		"two": {ActionCallResolveRequest{
			Started: &ResolveArmStarted{}, Result: json.RawMessage(`{}`),
		}, ""},
	}
	for name, tc := range cases {
		if got := tc.request.Arm(); got != tc.want {
			t.Errorf("%s: Arm() = %q, want %q", name, got, tc.want)
		}
	}
}

func TestHighestResolveReason(t *testing.T) {
	cases := map[string]struct {
		reasons []ResolveReason
		want    ResolveReason
	}{
		"none": {nil, ""},
		"one":  {[]ResolveReason{ReasonLateAcknowledgement}, ReasonLateAcknowledgement},
		"foreign sender outranks a repeat": {
			[]ResolveReason{ReasonRepeatedAcknowledgement, ReasonWrongResponder},
			ReasonWrongResponder,
		},
		"a settlement outranks a repeat": {
			[]ResolveReason{ReasonRepeatedAcknowledgement, ReasonAlreadyResolved},
			ReasonAlreadyResolved,
		},
		"a repeat outranks a late acknowledgement": {
			[]ResolveReason{ReasonLateAcknowledgement, ReasonRepeatedAcknowledgement},
			ReasonRepeatedAcknowledgement,
		},
		"an unknown interaction outranks everything": {
			[]ResolveReason{
				ReasonLateAcknowledgement, ReasonRepeatedAcknowledgement,
				ReasonAlreadyResolved, ReasonWrongResponder, ReasonUnknownInteraction,
			},
			ReasonUnknownInteraction,
		},
		"an unrecognized reason never wins": {
			[]ResolveReason{"invented", ReasonLateAcknowledgement},
			ReasonLateAcknowledgement,
		},
		"only unrecognized reasons name none": {[]ResolveReason{"invented"}, ""},
	}
	for name, tc := range cases {
		if got := HighestResolveReason(tc.reasons...); got != tc.want {
			t.Errorf("%s: HighestResolveReason = %q, want %q", name, got, tc.want)
		}
	}
	if _, ok := ResolveReasonRank("invented"); ok {
		t.Error("ResolveReasonRank accepted a reason outside the ladder")
	}
	for rank, reason := range resolveReasonLadder {
		got, ok := ResolveReasonRank(reason)
		if !ok || got != rank {
			t.Errorf("ResolveReasonRank(%q) = (%d, %v), want (%d, true)", reason, got, ok, rank)
		}
	}
}

func TestResolveRequestEncoding(t *testing.T) {
	encoded, err := json.Marshal(ActionCallResolveRequest{
		InteractionID: "i-1", SessionID: "s-1", RunID: "r-1", ToolCallID: "t-1",
		RequestedBy: "agent", RespondedBy: "control", Started: &ResolveArmStarted{},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"interaction_id":"i-1","session_id":"s-1","run_id":"r-1","tool_call_id":"t-1","requested_by":"agent","responded_by":"control","started":{}}`
	if string(encoded) != want {
		t.Errorf("encoded acknowledgement = %s, want %s", encoded, want)
	}
	encoded, err = json.Marshal(ActionCallResolveResponse{
		InteractionID: "i-1", SessionID: "s-1", RunID: "r-1", ToolCallID: "t-1", Accepted: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	want = `{"interaction_id":"i-1","session_id":"s-1","run_id":"r-1","tool_call_id":"t-1","accepted":true}`
	if string(encoded) != want {
		t.Errorf("encoded acceptance = %s, want %s", encoded, want)
	}
}
