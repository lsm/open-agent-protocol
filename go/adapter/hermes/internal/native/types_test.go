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

func TestInteractionRequests(t *testing.T) {
	if _, err := decodeEvent(t, `{"type":"approval.request","session_id":"s","seq":2,"payload":{"command":"rm -rf /tmp/x","choices":["once","session","always","deny"]}}`); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeEvent(t, `{"type":"approval.request","session_id":"s","seq":2,"payload":{"command":"x","choices":["maybe"]}}`); err == nil {
		t.Fatal("unknown choice accepted")
	}
	if _, err := decodeEvent(t, `{"type":"approval.request","session_id":"s","seq":2,"payload":{"command":"x","request_id":"abcd1234"}}`); err == nil {
		t.Fatal("approval.request with request_id accepted")
	}
	if _, err := decodeEvent(t, `{"type":"clarify.request","session_id":"s","seq":2,"payload":{"request_id":"abcd1234","question":"which?","choices":["a","b"]}}`); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeEvent(t, `{"type":"clarify.request","session_id":"s","seq":2,"payload":{"request_id":"abcd1234","questions":[{"qid":"1","question":"q","choices":["a"]},{"qid":"2","question":"r","choices":["b"]}],"question":"both"}}`); err == nil {
		t.Fatal("both clarify forms accepted")
	}
	if _, err := decodeEvent(t, `{"type":"sudo.request","session_id":"s","seq":2,"payload":{"request_id":"abcd1234"}}`); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeEvent(t, `{"type":"secret.request","session_id":"s","seq":2,"payload":{"request_id":"abcd1234","prompt":"key?","env_var":"TOKEN"}}`); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeEvent(t, `{"type":"secret.expire","session_id":"s","seq":2,"payload":{"request_id":"abcd1234"}}`); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeEvent(t, `{"type":"secret.expire","session_id":"s","seq":2,"payload":{}}`); err == nil {
		t.Fatal("expire without request_id accepted")
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
	if err := DecodeStrict([]byte(`{"status":"ok","remaining":["q2"]}`), &RespondResult{}); err != nil {
		t.Fatal(err)
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
