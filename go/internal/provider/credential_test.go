package provider

import (
	"errors"
	"testing"
)

func anonymousModel(provider string) Model {
	return Model{ID: "m", Provider: provider, AllowsAnonymous: true, HasBaseURL: true, BaseURL: "http://127.0.0.1:1"}
}

func TestTheCallersKeyWinsAndAnEmptyOneFallsThrough(t *testing.T) {
	model := anonymousModel("ollama")
	fromEnv := func(string) (Credential, bool) {
		return Credential{Key: "from-env", Source: SourceEnvironment}, true
	}
	key, err := ResolveAPIKey(model, KeyOptions{Key: "from-caller"}, fromEnv)
	if err != nil || key != "from-caller" {
		t.Errorf("key = %q, err = %v, want the caller's own key", key, err)
	}
	key, err = ResolveAPIKey(model, KeyOptions{Key: ""}, fromEnv)
	if err != nil || key != "from-env" {
		t.Errorf("key = %q, err = %v, want the environment's: an empty caller key falls through rather than winning", key, err)
	}
}

func TestAnEnvironmentVariableThatIsSetButEmptyIsNotAnAnonymousGrant(t *testing.T) {
	model := Model{ID: "m", Provider: "ollama", HasBaseURL: true, BaseURL: "http://127.0.0.1:1"}
	fromEnv := func(string) (Credential, bool) {
		return Credential{Key: "", Source: SourceEnvironment, Name: "EMPTY"}, true
	}
	if _, err := ResolveAPIKey(model, KeyOptions{}, fromEnv); !errors.Is(err, ErrMissingAPIKey) {
		t.Errorf("err = %v, want a missing key: an empty value falls through to the anonymous rule, which the model does not reach", err)
	}
	allowed := anonymousModel("ollama")
	key, err := ResolveAPIKey(allowed, KeyOptions{}, fromEnv)
	if err != nil || key != "" {
		t.Errorf("key = %q, err = %v, want the anonymous rule to supply the empty key", key, err)
	}
}

func TestTheAnonymousRuleAppliesOnlyWhenTheModelAndTheVendorBothAllowIt(t *testing.T) {
	allowed := anonymousModel("ollama")
	if key, err := ResolveAPIKey(allowed, KeyOptions{}, nil); err != nil || key != "" {
		t.Errorf("key = %q, err = %v, want the empty key", key, err)
	}
	for _, vendor := range []string{"openai", "deepseek", "kimi", "github-copilot"} {
		blocked := anonymousModel(vendor)
		if _, err := ResolveAPIKey(blocked, KeyOptions{}, nil); !errors.Is(err, ErrMissingAPIKey) {
			t.Errorf("%s: err = %v, want a missing key even though the model allows anonymity", vendor, err)
		}
	}
	notAllowed := anonymousModel("ollama")
	notAllowed.AllowsAnonymous = false
	if _, err := ResolveAPIKey(notAllowed, KeyOptions{}, nil); !errors.Is(err, ErrMissingAPIKey) {
		t.Errorf("err = %v, want a missing key: the model's own flag is off", err)
	}
}

func TestAMissingKeyIsReportedAsSuch(t *testing.T) {
	model := Model{ID: "m", Provider: "openai", HasBaseURL: true, BaseURL: "https://api.openai.com"}
	_, err := ResolveAPIKey(model, KeyOptions{}, nil)
	if !errors.Is(err, ErrMissingAPIKey) {
		t.Fatalf("err = %v, want ErrMissingAPIKey", err)
	}
	if err.Error() != "missing api key" {
		t.Errorf("the message = %q, want the spelling zig's error carries", err.Error())
	}
}

func TestTheAuthorizationValueIsTheBearerPrefixAndNothingElse(t *testing.T) {
	if got := BearerValue("sk-abc"); got != "Bearer sk-abc" {
		t.Errorf("got %q, want the bearer prefix with no other scheme", got)
	}
	if got := BearerValue(""); got != "" {
		t.Errorf("an empty key = %q, want no header value at all", got)
	}
	if got := BearerValue("  sk-padded  "); got != "Bearer   sk-padded  " {
		t.Errorf("got %q, want the key untrimmed: zig builds the value by concatenation", got)
	}
}
