package validation_test

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/go/validation"
)

func presentationTrace(t *testing.T, envelopes ...string) validation.Result {
	t.Helper()
	v, err := validation.NewPresentationValidator()
	if err != nil {
		t.Fatalf("new presentation validator: %v", err)
	}
	return v.Validate(strings.NewReader("["+strings.Join(envelopes, ",")+"]"), "trace.json")
}

func presentationSnapshot(id, session string, revision int) string {
	return `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.presentation-control","type":"presentation.snapshot.response","id":"` + id + `","in_reply_to":"req-1","payload":{"snapshot":{"target":{"kind":"session","session_id":"` + session + `"},"revision":` + strconv.Itoa(revision) + `,"state":{"session":{"session_id":"` + session + `","status":"idle"},"timeline":[],"composer":{"session_id":"` + session + `","delivery":"auto","enabled":true},"affordances":[],"pending_prompts":[],"diagnostics":[]}}}}`
}

func presentationUpdate(id, session string, sequence, base, revision int, changes string) string {
	return `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.presentation-control","type":"presentation.updated","id":"` + id + `","sequence":` + strconv.Itoa(sequence) + `,"payload":{"target":{"kind":"session","session_id":"` + session + `"},"base_revision":` + strconv.Itoa(base) + `,"revision":` + strconv.Itoa(revision) + `,"changes":[` + changes + `]}}`
}

const idleChange = `{"kind":"session.status.set","status":"idle","active_run_id":null}`

func TestPresentationUpdateApplyingToTheRevisionItNames(t *testing.T) {
	result := presentationTrace(t,
		presentationSnapshot("s1", "sess-1", 1),
		presentationUpdate("u1", "sess-1", 1, 1, 2, idleChange),
	)
	if diags := codes(result); len(diags) != 0 {
		t.Fatalf("an update contiguous with the snapshot it follows was rejected: %v", diags)
	}
}

func TestPresentationUpdateAgainstARevisionNoSnapshotPublished(t *testing.T) {
	result := presentationTrace(t, presentationUpdate("u1", "sess-1", 1, 1, 2, idleChange))
	if !hasCode(result, validation.CodePresentationUpdateWithoutSnapshot) {
		t.Fatalf("an update against an unpublished revision was admitted: %v", codes(result))
	}
}

func TestPresentationUpdateNamingABaseRevisionTheTargetNeverReached(t *testing.T) {
	result := presentationTrace(t,
		presentationSnapshot("s1", "sess-1", 4),
		presentationUpdate("u1", "sess-1", 1, 2, 5, idleChange),
	)
	if !hasCode(result, validation.CodePresentationBaseRevisionMismatch) {
		t.Fatalf("an update naming a base_revision the target had not reached was admitted: %v", codes(result))
	}
}

func TestPresentationUpdateNotAdvancingTheRevisionByOne(t *testing.T) {
	result := presentationTrace(t,
		presentationSnapshot("s1", "sess-1", 4),
		presentationUpdate("u1", "sess-1", 1, 4, 7, idleChange),
	)
	if !hasCode(result, validation.CodePresentationRevisionGap) {
		t.Fatalf("an update skipping revisions was admitted: %v", codes(result))
	}
}

func TestPresentationSnapshotGoingBackOnTheRevisionItsTargetPublished(t *testing.T) {
	result := presentationTrace(t,
		presentationSnapshot("s1", "sess-1", 7),
		presentationSnapshot("s2", "sess-1", 3),
	)
	if !hasCode(result, validation.CodePresentationRevisionRegression) {
		t.Fatalf("a snapshot regressing its target's revision was admitted: %v", codes(result))
	}
}

func TestPresentationRevisionIsScopedToItsTarget(t *testing.T) {
	result := presentationTrace(t,
		presentationSnapshot("s1", "sess-1", 7),
		presentationSnapshot("s2", "sess-2", 1),
	)
	if diags := codes(result); len(diags) != 0 {
		t.Fatalf("a second target's first revision was read as a regression on the first: %v", diags)
	}
}

func TestPresentationUpdateSequenceOpensAtOneAndStaysContiguous(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		sequence []int
		want     string
	}{
		{"first update does not open at one", []int{2, 3}, validation.CodeSequenceGap},
		{"a repeated sequence", []int{1, 1}, validation.CodeSequenceRegression},
		{"a sequence going back", []int{1, 3, 2}, validation.CodeSequenceRegression},
		{"a skipped sequence", []int{1, 3}, validation.CodeSequenceGap},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			envelopes := []string{presentationSnapshot("s1", "sess-1", 1)}
			revision := 1
			for i, sequence := range testCase.sequence {
				revision++
				envelopes = append(envelopes, presentationUpdate("u"+strconv.Itoa(i), "sess-1", sequence, revision-1, revision, idleChange))
			}
			result := presentationTrace(t, envelopes...)
			if !hasCode(result, testCase.want) {
				t.Fatalf("a broken update sequence was admitted: %v", codes(result))
			}
		})
	}
}

func TestPresentationAcceptedSubmitMustResolveItsDelivery(t *testing.T) {
	request := `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.presentation-control","type":"intent.message.submit.request","id":"q1","payload":{"intent_id":"i1","session_id":"sess-1","message":{"role":"user","content":"hi"},"delivery":"auto"}}`
	unresolved := `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.presentation-control","type":"intent.message.submit.response","id":"r1","in_reply_to":"q1","payload":{"intent_id":"i1","accepted":true,"requested_delivery":"auto","effective_delivery":null,"admission":"started"}}`
	if !hasCode(presentationTrace(t, request, unresolved), validation.CodePresentationDeliveryUnresolved) {
		t.Fatal("an accepted submit reporting no effective delivery was admitted")
	}
	resolved := `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.presentation-control","type":"intent.message.submit.response","id":"r2","in_reply_to":"q1","payload":{"intent_id":"i1","accepted":true,"requested_delivery":"auto","effective_delivery":"start","delivery_resolution":"session_idle","admission":"started"}}`
	if diags := codes(presentationTrace(t, request, resolved)); len(diags) != 0 {
		t.Fatalf("an accepted submit that resolved its delivery was rejected: %v", diags)
	}
}

func TestPresentationCoreProfileEnvelopesAreNotCoreEnvelopes(t *testing.T) {
	core, err := validation.New()
	if err != nil {
		t.Fatalf("new core validator: %v", err)
	}
	result := core.Validate(strings.NewReader("["+presentationSnapshot("s1", "sess-1", 1)+"]"), "trace.json")
	if len(result.Diagnostics) == 0 {
		t.Fatal("a presentation envelope was accepted by the agent-control-core validator")
	}
}

func TestPresentationManifestEntriesRunUnderThePresentationValidator(t *testing.T) {
	root := filepath.Join("..", "..")
	manifest := filepath.Join(root, "fixtures", "manifest.json")
	core, err := validation.New()
	if err != nil {
		t.Fatalf("new core validator: %v", err)
	}
	outcomes, err := core.ValidateManifest(manifest)
	if err != nil {
		t.Fatalf("validate manifest: %v", err)
	}
	presentation := 0
	for _, outcome := range outcomes {
		if strings.HasPrefix(outcome.Entry.Path, "presentation/") {
			presentation++
		}
	}
	if presentation < 8 {
		t.Fatalf("the manifest exercised %d presentation fixtures, want at least 8", presentation)
	}
}
