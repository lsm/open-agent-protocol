package native

import (
	"encoding/json"
	"strings"
	"testing"
)

const testEpoch = "0123456789abcdef0123456789abcdef"

func decodeEvent(t *testing.T, frame string) (*Event, error) {
	t.Helper()
	value, err := DecodeNotification(NotifyEvent, []byte(frame))
	if err != nil {
		return nil, err
	}
	event, ok := value.(*Event)
	if !ok {
		t.Fatalf("decoded %T, want *Event", value)
	}
	return event, nil
}

func TestReadyFrameIsValidated(t *testing.T) {
	ready := `{"type":"gateway.ready","payload":{"skin":{"name":"default"},"change_events":true,"replay_epoch":"` + testEpoch + `"}}`
	if _, err := decodeEvent(t, ready); err != nil {
		t.Fatal(err)
	}
	for name, epoch := range map[string]string{
		"short epoch":   "abcd",
		"non-hex epoch": "zzzz456789abcdef0123456789abcdef",
		"upper hex":     "0123456789ABCDEF0123456789abcdef",
	} {
		if _, err := decodeEvent(t, strings.Replace(ready, testEpoch, epoch, 1)); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	for name, frame := range map[string]string{
		"no change_events": strings.Replace(ready, `"change_events":true`, `"change_events":false`, 1),
		"no skin":          strings.Replace(ready, `"skin":{"name":"default"},`, ``, 1),
		"session-scoped":   strings.Replace(ready, `{"type":`, `{"session_id":"abcd1234","seq":1,"type":`, 1),
	} {
		if _, err := decodeEvent(t, frame); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

func TestSessionSequencingRules(t *testing.T) {
	if _, err := decodeEvent(t, `{"type":"message.start","session_id":"abcd1234"}`); err == nil {
		t.Fatal("session event without seq accepted")
	}
	if _, err := decodeEvent(t, `{"type":"message.start","session_id":"abcd1234","seq":0}`); err == nil {
		t.Fatal("zero seq accepted")
	}
	if _, err := decodeEvent(t, `{"type":"skin.changed","payload":{},"seq":1}`); err == nil {
		t.Fatal("session-less event with seq accepted")
	}
	if _, err := decodeEvent(t, `{"type":"message.start","session_id":"abcd1234","seq":1,"payload":{"text":"x"}}`); err == nil {
		t.Fatal("message.start with payload accepted")
	}
	if _, err := decodeEvent(t, `{"type":"message.start","session_id":"abcd1234","seq":1}`); err != nil {
		t.Fatal(err)
	}
}

func TestMessageCompleteStatuses(t *testing.T) {
	settled := func(status string) string {
		return `{"type":"message.complete","session_id":"s","seq":3,"payload":{"text":"hi","usage":{"model":"m","input":1,"output":2,"reasoning":0,"prompt":1,"completion":2,"total":3,"calls":1},"status":` + status + `}}`
	}
	for _, status := range []string{`"complete"`, `"interrupted"`, `"error"`} {
		frame := settled(status)
		if status == `"error"` {
			frame = strings.Replace(frame, `"status":"error"`, `"status":"error","error":"boom","recoverable":true`, 1)
		}
		if _, err := decodeEvent(t, frame); err != nil {
			t.Fatalf("status %s: %v", status, err)
		}
	}
	if _, err := decodeEvent(t, `{"type":"message.complete","session_id":"s","seq":3,"payload":{"text":"x","status":"error","error":"boom"}}`); err != nil {
		t.Fatalf("usage-less compute-host error shape rejected: %v", err)
	}
	if _, err := decodeEvent(t, `{"type":"message.complete","session_id":"s","seq":3,"payload":{"text":"x","status":"error"}}`); err == nil {
		t.Fatal("error settlement without message accepted")
	}
	if _, err := decodeEvent(t, `{"type":"message.complete","session_id":"s","seq":3,"payload":{"text":"mirror"}}`); err != nil {
		t.Fatalf("child-mirror status-less variant rejected: %v", err)
	}
	if _, err := decodeEvent(t, `{"type":"message.complete","session_id":"s","seq":3,"payload":{"text":"x","status":"weird"}}`); err == nil {
		t.Fatal("unknown status accepted")
	}
}

func TestDeltaVariantExclusivity(t *testing.T) {
	if _, err := decodeEvent(t, `{"type":"message.delta","session_id":"s","seq":1,"payload":{"text":"hi","rendered":"\u001b[0m"}}`); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeEvent(t, `{"type":"reasoning.delta","session_id":"s","seq":1,"payload":{"text":"hmm","rendered":"x"}}`); err == nil {
		t.Fatal("rendered on reasoning.delta accepted")
	}
	if _, err := decodeEvent(t, `{"type":"thinking.delta","session_id":"s","seq":1,"payload":{"text":"hmm","verbose":true}}`); err == nil {
		t.Fatal("verbose on thinking.delta accepted")
	}
	if _, err := decodeEvent(t, `{"type":"reasoning.delta","session_id":"s","seq":1,"payload":{"text":"hmm","verbose":true}}`); err != nil {
		t.Fatal(err)
	}
}

func TestServerRequestsDecode(t *testing.T) {
	approval := `{"session_id":"s","command":"rm -rf /tmp/x","pattern_key":"delete in root path","pattern_keys":["delete in root path"],"description":"delete in root path","allow_permanent":true,"allow_session":true,"request_id":"4057b948aca048909e7b0850c5190fa3","choices":["once","session","always","deny"]}`
	value, err := DecodeServerRequest(RequestApproval, []byte(approval))
	if err != nil {
		t.Fatal(err)
	}
	if request := value.(*ApprovalRequestParams); request.Command != "rm -rf /tmp/x" || len(request.Choices) != 4 || request.AllowSession == nil || !*request.AllowSession {
		t.Fatalf("approval = %+v", request)
	}
	if _, err := DecodeServerRequest(RequestApproval, []byte(strings.Replace(approval, `"deny"]`, `"maybe"]`, 1))); err == nil {
		t.Fatal("unknown approval choice accepted")
	}
	if _, err := DecodeServerRequest(RequestApproval, []byte(strings.Replace(approval, `"request_id":"4057b948aca048909e7b0850c5190fa3",`, ``, 1))); err == nil {
		t.Fatal("approval without its queue request_id accepted")
	}
	if _, err := DecodeServerRequest(RequestClarify, []byte(`{"session_id":"s","question":"which?","choices":["a (Recommended)","b"]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeServerRequest(RequestClarify, []byte(`{"session_id":"s","questions":[{"qid":"q0","question":"first?","choices":["a (Recommended)","b"],"multi_select":false}]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeServerRequest(RequestClarify, []byte(`{"session_id":"s","question":"both","questions":[{"qid":"q0","question":"q","choices":["a"]}]}`)); err == nil {
		t.Fatal("both clarify forms accepted")
	}
	if _, err := DecodeServerRequest(RequestClarify, []byte(`{"session_id":"s","question":"which?","request_id":"abcd1234"}`)); err == nil {
		t.Fatal("clarify carrying the retired request_id accepted")
	}
	if _, err := DecodeServerRequest(RequestSudo, []byte(`{"session_id":"s","command":"sudo true"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeServerRequest(RequestSecret, []byte(`{"session_id":"s","prompt":"CI token","env_var":"CI_TOKEN","metadata":{"skill_name":"probe-secret"}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeServerRequest(RequestSecret, []byte(`{"session_id":"s","prompt":"CI token"}`)); err == nil {
		t.Fatal("secret without env_var accepted")
	}
	if value, err := DecodeServerRequest("tour", []byte(`{"session_id":"s"}`)); value != nil || err != nil {
		t.Fatalf("unmapped server request = %v, %v", value, err)
	}
}

func TestRequestCancelIsValidated(t *testing.T) {
	if _, err := decodeEvent(t, `{"type":"request.cancel","session_id":"s","seq":10,"payload":{"id":"srq-d899e57c7e38","method":"clarify","reason":"interrupted"}}`); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeEvent(t, `{"type":"request.cancel","session_id":"s","seq":10,"payload":{"method":"clarify","reason":"timeout"}}`); err == nil {
		t.Fatal("request.cancel without id accepted")
	}
	for _, retired := range []string{"approval.request", "clarify.request", "clarify.expire", "sudo.request", "secret.expire"} {
		if _, err := decodeEvent(t, `{"type":"`+retired+`","session_id":"s","seq":2,"payload":{}}`); err == nil {
			t.Fatalf("retired event %s accepted", retired)
		}
	}
}

func TestInterruptedSettlementCarriesNullTextAndPersistedTurn(t *testing.T) {
	frame := `{"type":"message.complete","session_id":"s","seq":4,"payload":{"text":null,"usage":{"model":"m","input":0,"output":0,"reasoning":0,"prompt":0,"completion":0,"total":0,"calls":0,"compressions":0,"active_subagents":0},"status":"interrupted","persisted_turn":{"row_ids":[1,2],"complete":false,"user_row_id":1}}}`
	event, err := decodeEvent(t, frame)
	if err != nil {
		t.Fatal(err)
	}
	var payload MessageCompletePayload
	if err := DecodeStrict(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Text != "" || payload.Status != "interrupted" || len(payload.PersistedTurn) == 0 {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestEventVocabularyClosure(t *testing.T) {
	for _, observed := range []string{"skin.changed", "subagent.tool", "voice.transcript", "sessions.changed", "todo.updated"} {
		if _, err := decodeEvent(t, `{"type":"`+observed+`","session_id":"s","seq":4,"payload":{"anything":true}}`); err != nil {
			t.Fatalf("observed %s rejected: %v", observed, err)
		}
	}
	if _, err := decodeEvent(t, `{"type":"future/unknown","session_id":"s","seq":4,"payload":{}}`); err == nil {
		t.Fatal("unknown event type accepted")
	}
	if _, err := DecodeNotification("something.else", []byte(`{}`)); err == nil {
		t.Fatal("non-event notification method accepted")
	}
}

func TestSessionInfoDecodesLeniently(t *testing.T) {
	frame := `{"type":"session.info","session_id":"s","seq":9,"payload":{"model":"m","provider":"p","running":true,"turn_started_at":123.5,"title":"t","stored_session_id":"20260908_142512_ab12cd","usage":{"model":"m","input":1,"output":1,"reasoning":0,"prompt":1,"completion":1,"total":2,"calls":1},"cwd":"/tmp","branch":"main","extra_future_field":123}}`
	event, err := decodeEvent(t, frame)
	if err != nil {
		t.Fatal(err)
	}
	var info SessionInfoPayload
	if err := json.Unmarshal(event.Payload, &info); err != nil {
		t.Fatal(err)
	}
	if !info.Running || info.StoredSessionID == "" || info.TurnStartedAt == nil || *info.TurnStartedAt != 123.5 {
		t.Fatalf("info = %+v", info)
	}
}

func TestSubagentCompleteValidation(t *testing.T) {
	frame := `{"type":"subagent.complete","session_id":"s","seq":7,"payload":{"goal":"g","task_count":1,"task_index":0,"subagent_id":"sub1","child_session_id":"c1","status":"complete","summary":"done","input_tokens":10,"output_tokens":5,"files_read":["a"]}}`
	if _, err := decodeEvent(t, frame); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeEvent(t, `{"type":"subagent.complete","session_id":"s","seq":7,"payload":{"goal":"g","task_count":1,"task_index":0,"cost_usd":0.5}}`); err == nil {
		t.Fatal("cost_usd accepted (dropped at the gateway boundary)")
	}
}

func TestResultShapesDecode(t *testing.T) {
	var submit PromptSubmitResult
	if err := DecodeStrict([]byte(`{"status":"streaming","survivor_user_row_ids":[3,null,5]}`), &submit); err != nil {
		t.Fatal(err)
	}
	if submit.Status != SubmitStreaming || len(submit.SurvivorUserRowIDs) != 3 || submit.SurvivorUserRowIDs[1] != nil {
		t.Fatalf("submit = %+v", submit)
	}
	var since EventsSinceResult
	if err := DecodeStrict([]byte(`{"events":[],"latest_seq":0,"truncated":false,"count":0,"epoch":"`+testEpoch+`"}`), &since); err != nil {
		t.Fatal(err)
	}
	withOpen := `{"events":[],"latest_seq":0,"truncated":false,"count":0,"epoch":"` + testEpoch + `","open_requests":[{"id":"srq-0123456789ab","method":"clarify","params":{"session_id":"s","question":"q"}}]}`
	if err := DecodeStrict([]byte(withOpen), &since); err != nil || len(since.OpenRequests) != 1 {
		t.Fatalf("open_requests = %+v err=%v", since, err)
	}

	var steer SteerResult
	if err := DecodeStrict([]byte(`{"status":"rejected"}`), &steer); err != nil || steer.Status != "rejected" {
		t.Fatalf("steer = %+v err=%v", steer, err)
	}
	if err := DecodeStrict([]byte(`{"status":"queued","text":"later"}`), &steer); err != nil || steer.Status != "queued" {
		t.Fatalf("steer = %+v err=%v", steer, err)
	}
	var subagentSteer SubagentSteerResult
	if err := DecodeStrict([]byte(`{"status":"ok","subagent_id":"sa1","text":"redirected"}`), &subagentSteer); err != nil || subagentSteer.SubagentID != "sa1" {
		t.Fatalf("subagent steer = %+v err=%v", subagentSteer, err)
	}
	var subagentInterrupt SubagentInterruptResult
	if err := DecodeStrict([]byte(`{"found":true,"subagent_id":"sa1"}`), &subagentInterrupt); err != nil || !subagentInterrupt.Found {
		t.Fatalf("subagent interrupt = %+v err=%v", subagentInterrupt, err)
	}
	window := `{"events":[{"type":"message.start","session_id":"s","seq":1},{"type":"message.delta","session_id":"s","seq":2,"payload":{"text":"x"}}],"latest_seq":2,"truncated":false,"count":2,"epoch":"` + testEpoch + `"}`
	if err := DecodeStrict([]byte(window), &since); err != nil || len(since.Events) != 2 || since.Count != 2 {
		t.Fatalf("replay window = %+v err=%v", since, err)
	}
	truncated := `{"events":[],"latest_seq":600,"truncated":true,"count":0,"epoch":"` + testEpoch + `"}`
	if err := DecodeStrict([]byte(truncated), &since); err != nil || !since.Truncated || since.LatestSeq != 600 {
		t.Fatalf("replay truncated = %+v err=%v", since, err)
	}
	if err := ValidateReady(&ReadyPayload{ChangeEvents: true, ReplayEpoch: testEpoch}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateReady(&ReadyPayload{ChangeEvents: true, ReplayEpoch: "e3b0c44298fc1c149afbf4c8996fb925"}); err != nil {
		t.Fatal(err)
	}
}

func TestMessageCompleteUsageToleratesExtensibleReadouts(t *testing.T) {
	frame := `{"type":"message.complete","session_id":"s","seq":3,"payload":{"text":"hi","usage":{"model":"m","input":1,"output":2,"reasoning":0,"prompt":1,"completion":2,"total":3,"calls":1,"active_subagents":2,"avg_latency_s":1.4,"avg_tps":9.2,"future_readout":true},"status":"complete"}}`
	event, err := decodeEvent(t, frame)
	if err != nil {
		t.Fatalf("extensible usage readouts rejected: %v", err)
	}
	var payload MessageCompletePayload
	if err := DecodeStrict(event.Payload, &payload); err != nil {
		t.Fatalf("payload strict decode: %v", err)
	}
	if payload.Usage.ActiveSubagents == nil || *payload.Usage.ActiveSubagents != 2 {
		t.Fatalf("active_subagents = %v", payload.Usage.ActiveSubagents)
	}

	if _, err := decodeEvent(t, `{"type":"message.complete","session_id":"s","seq":3,"payload":{"text":"hi","usage":{"model":"m","input":1,"output":2,"reasoning":0,"prompt":1,"completion":2,"total":3,"calls":1},"status":"complete","surprise":1}}`); err == nil {
		t.Fatal("unknown field on message.complete accepted")
	}
}

func TestErrorSurfaceCarriesProviderIdentity(t *testing.T) {
	frame := `{"type":"message.complete","session_id":"s","seq":9,"payload":{"text":"Error: boom","usage":{},"status":"error","error":"boom","recoverable":true,"error_surface":{"layer":"provider","code":"auth","retryable":false,"provider":"openai","model":"gpt-5.6-sol"}}}`
	event, err := decodeEvent(t, frame)
	if err != nil {
		t.Fatalf("error_surface with provider identity rejected: %v", err)
	}
	var payload MessageCompletePayload
	if err := DecodeStrict(event.Payload, &payload); err != nil {
		t.Fatalf("payload strict decode: %v", err)
	}
	if payload.ErrorSurface == nil || payload.ErrorSurface.Provider != "openai" || payload.ErrorSurface.Model != "gpt-5.6-sol" {
		t.Fatalf("error_surface = %+v", payload.ErrorSurface)
	}
}
