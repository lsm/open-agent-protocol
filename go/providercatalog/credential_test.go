package providercatalog

import (
	"testing"

	"github.com/lsm/open-agent-protocol/providers"
)

func env(names ...string) []EnvironmentValue {
	held := make([]EnvironmentValue, 0, len(names))
	for _, name := range names {
		held = append(held, EnvironmentValue{Name: name, Value: "held"})
	}
	return held
}

func TestTheEnvironmentAnswersBeforeAStoredKey(t *testing.T) {
	catalog := heldCatalog(t)
	stored := "sk-stored"
	held := StoredCredential{APIKey: &stored}
	credential, ok := LookupCredential(catalog, env("OPENAI_API_KEY"), &held, "openai")
	if !ok {
		t.Fatal("openai resolves no credential")
	}
	if credential.Source != SourceEnvironment {
		t.Errorf("source = %q, want %q: the environment answers first", credential.Source, SourceEnvironment)
	}
	if credential.Name != "OPENAI_API_KEY" {
		t.Errorf("name = %q, want the variable that held the key", credential.Name)
	}
	if credential.Key != "held" {
		t.Errorf("key = %q, want the value the environment held", credential.Key)
	}
}

func TestTheRowsOwnVariableOrderDecidesWhichKeyAnswers(t *testing.T) {
	catalog := heldCatalog(t)
	names := CredentialEnv(catalog, "anthropic")
	if len(names) != 2 || names[0] != "ANTHROPIC_AUTH_TOKEN" || names[1] != "ANTHROPIC_API_KEY" {
		t.Fatalf("anthropic's variables = %v, want the auth token before the api key: the order rule is untested", names)
	}
	credential, ok := APIKeyForProvider(catalog, env("ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"), "anthropic")
	if !ok {
		t.Fatal("anthropic resolves no credential")
	}
	if credential.Name != "ANTHROPIC_AUTH_TOKEN" {
		t.Errorf("name = %q, want the row's first variable, not the one set first", credential.Name)
	}
}

func TestAnEmptyEnvironmentVariableCountsAsUnset(t *testing.T) {
	catalog := heldCatalog(t)
	names := CredentialEnv(catalog, "anthropic")
	if len(names) != 2 {
		t.Fatalf("anthropic's variables = %v, want two: the fallback is untested", names)
	}
	held := []EnvironmentValue{
		{Name: "ANTHROPIC_AUTH_TOKEN", Value: ""},
		{Name: "ANTHROPIC_API_KEY", Value: "sk-second"},
	}
	credential, ok := APIKeyForProvider(catalog, held, "anthropic")
	if !ok {
		t.Fatal("an empty variable leaves the row with no key: the next variable is tried")
	}
	if credential.Name != "ANTHROPIC_API_KEY" || credential.Key != "sk-second" {
		t.Errorf("credential = %+v, want the second variable's key", credential)
	}
	if _, ok := APIKeyFromNames([]EnvironmentValue{{Name: "A", Value: ""}}, []string{"A", "B"}); ok {
		t.Error("a variable that is set and empty resolves a key")
	}
	repeated := []EnvironmentValue{{Name: "A", Value: ""}, {Name: "A", Value: "later"}}
	if _, ok := APIKeyFromNames(repeated, []string{"A"}); ok {
		t.Error("the first value held for a name decides it: a later value for the same name is not read")
	}
}

func TestAStoredKeyAnswersARowWithNothingInTheEnvironment(t *testing.T) {
	catalog := heldCatalog(t)
	stored := "sk-stored"
	credential, ok := LookupCredential(catalog, nil, &StoredCredential{APIKey: &stored}, "openai")
	if !ok {
		t.Fatal("openai resolves no credential with a stored key and no environment")
	}
	if credential.Source != SourceStored {
		t.Errorf("source = %q, want %q", credential.Source, SourceStored)
	}
	if credential.Name != "openai" {
		t.Errorf("name = %q, want the row's id: a stored key names no variable", credential.Name)
	}
	if credential.Key != stored {
		t.Errorf("key = %q, want the stored key", credential.Key)
	}
}

func TestAStoredKeyIsRefusedByARowThatTakesNoApiKey(t *testing.T) {
	catalog := heldCatalog(t)
	if !AcceptsAuth(catalog, "openai-codex", "oauth") || AcceptsAuth(catalog, "openai-codex", "api_key") {
		t.Fatalf("openai-codex auth = %v, want oauth and not api_key: the refusal is untested", catalog.Providers)
	}
	if CredentialEnv(catalog, "openai-codex") != nil {
		t.Fatal("openai-codex names a variable, so the environment would answer first")
	}
	stored := "sk-stored"
	if _, ok := LookupCredential(catalog, nil, &StoredCredential{APIKey: &stored}, "openai-codex"); ok {
		t.Error("an api key answers a row that takes no api key")
	}
	access := "at-oauth"
	credential, ok := LookupCredential(catalog, nil, &StoredCredential{OAuthAccess: &access}, "openai-codex")
	if !ok {
		t.Fatal("an oauth access answers no credential for a row that takes oauth")
	}
	if credential.Source != SourceOAuth {
		t.Errorf("source = %q, want %q", credential.Source, SourceOAuth)
	}
}

func TestAnOAuthAccessIsRefusedByAnApiKeyOnlyRow(t *testing.T) {
	catalog := heldCatalog(t)
	if AcceptsAuth(catalog, "openai", "oauth") {
		t.Fatal("openai takes oauth, so the refusal is untested")
	}
	access := "at-oauth"
	if _, ok := LookupCredential(catalog, nil, &StoredCredential{OAuthAccess: &access}, "openai"); ok {
		t.Error("an oauth access answers a row that takes no oauth")
	}
}

func TestAStoredKeyThatIsSetAndEmptyResolvesNothing(t *testing.T) {
	catalog := heldCatalog(t)
	empty := ""
	if _, ok := LookupCredential(catalog, nil, &StoredCredential{APIKey: &empty}, "openai"); ok {
		t.Error("a stored key that is empty resolves a credential")
	}
	if _, ok := LookupCredential(catalog, nil, &StoredCredential{OAuthAccess: &empty}, "openai-codex"); ok {
		t.Error("a stored oauth access that is empty resolves a credential")
	}
	if _, ok := LookupCredential(catalog, nil, &StoredCredential{}, "openai"); ok {
		t.Error("a storage with nothing in it resolves a credential")
	}
}

func TestARowThatNeedsNoCredentialSaysSo(t *testing.T) {
	catalog := heldCatalog(t)
	var found int
	for _, provider := range catalog.Providers {
		if !NeedsNoCredential(catalog, provider.ID) {
			continue
		}
		found++
		if !AcceptsAuth(catalog, provider.ID, "none") {
			t.Errorf("%s needs no credential but does not record the kind that says so", provider.ID)
		}
	}
	if found == 0 {
		t.Error("no row records that it needs no credential: the case is untested")
	}
	if NeedsNoCredential(catalog, "openai") {
		t.Error("openai needs a credential")
	}
	if NeedsNoCredential(catalog, "no-such-provider") {
		t.Error("an unknown id needs a credential")
	}
}

func TestAnUnknownIdResolvesNoCredential(t *testing.T) {
	catalog := heldCatalog(t)
	stored := "sk-stored"
	held := &StoredCredential{APIKey: &stored}
	if _, ok := LookupCredential(catalog, env("OPENAI_API_KEY", "ANYTHING"), held, "no-such-provider"); ok {
		t.Error("an unknown id resolves a credential")
	}
	if _, ok := APIKeyForProvider(catalog, env("OPENAI_API_KEY"), "no-such-provider"); ok {
		t.Error("an unknown id resolves a key from the environment")
	}
}

func TestEveryRowThatNamesAVariableResolvesIt(t *testing.T) {
	catalog := heldCatalog(t)
	for _, provider := range catalog.Providers {
		names := CredentialEnv(catalog, provider.ID)
		if len(names) == 0 {
			continue
		}
		held := make([]EnvironmentValue, 0, len(names))
		for _, name := range names {
			held = append(held, EnvironmentValue{Name: name, Value: "key-for-" + name})
		}
		credential, ok := APIKeyForProvider(catalog, held, provider.ID)
		if !ok {
			t.Errorf("%s names %v and resolves no key from them", provider.ID, names)
			continue
		}
		if credential.Name != names[0] {
			t.Errorf("%s answered %q, want its first variable %q", provider.ID, credential.Name, names[0])
		}
	}
}

func realCatalog(t *testing.T) Catalog {
	t.Helper()
	catalog, err := Load(providers.Files)
	if err != nil {
		t.Fatalf("load the real catalog: %v", err)
	}
	return catalog
}

func storedKey(value string) *StoredCredential {
	return &StoredCredential{APIKey: &value}
}

func TestARowThatNamesStoredFirstOutranksTheEnvironment(t *testing.T) {
	catalog := realCatalog(t)

	got := CredentialPrecedence(catalog, "kimi")
	if len(got) != 2 || got[0] != SourceStored || got[1] != SourceEnvironment {
		t.Fatalf("kimi precedence = %v, want [stored environment]: the row says so in providers/catalog.json", got)
	}

	held := []EnvironmentValue{{Name: "KIMI_API_KEY", Value: "from-the-environment"}}
	credential, ok := LookupCredential(catalog, held, storedKey("from-a-login"), "kimi")
	if !ok {
		t.Fatal("a row that prefers stored must still find one when both are held")
	}
	if credential.Source != SourceStored || credential.Key != "from-a-login" {
		t.Errorf("credential = %+v, want the stored one: the environment is signed with the env var, so listing models under the env var's region advertises models this login cannot call", credential)
	}
}

func TestARowWithNoPrecedenceKeepsTheEnvironmentFirst(t *testing.T) {
	catalog := realCatalog(t)

	row, known := findProvider(catalog, "openai")
	if !known {
		t.Fatal("the catalog should carry an openai row")
	}
	if len(row.CredentialOrder) != 0 {
		t.Fatalf("openai carries a precedence of %v, want none: it is the default that most rows are on", row.CredentialOrder)
	}

	got := CredentialPrecedence(catalog, "openai")
	if len(got) != 2 || got[0] != SourceEnvironment || got[1] != SourceStored {
		t.Fatalf("precedence = %v, want [environment stored]", got)
	}

	held := []EnvironmentValue{{Name: row.CredentialEnv[0], Value: "from-the-environment"}}
	credential, ok := LookupCredential(catalog, held, storedKey("from-a-login"), "openai")
	if !ok {
		t.Fatal("a row with no precedence should still find a credential when one is held")
	}
	if credential.Source != SourceEnvironment || credential.Key != "from-the-environment" {
		t.Errorf("credential = %+v, want the environment one: absent means environment first", credential)
	}
}

func TestTheSecondSourceIsTriedWhenTheFirstIsEmpty(t *testing.T) {
	catalog := realCatalog(t)

	empty := ""
	cases := []struct {
		name   string
		held   []EnvironmentValue
		stored *StoredCredential
		id     string
		want   Source
	}{
		{"kimi falls through to the environment when nothing is stored", []EnvironmentValue{{Name: "KIMI_API_KEY", Value: "from-the-environment"}}, nil, "kimi", SourceEnvironment},
		{"kimi falls through to the environment when the stored key is empty", []EnvironmentValue{{Name: "KIMI_API_KEY", Value: "from-the-environment"}}, storedKey(empty), "kimi", SourceEnvironment},
		{"a default row falls through to stored when the environment holds nothing", nil, storedKey("from-a-login"), "openai", SourceStored},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			credential, ok := LookupCredential(catalog, c.held, c.stored, c.id)
			if !ok {
				t.Fatalf("no credential found, want one from %q", c.want)
			}
			if credential.Source != c.want {
				t.Errorf("credential = %+v, want source %q", credential, c.want)
			}
		})
	}
}

func TestAPrecedenceTheRowDoesNotRecogniseFallsBackToTheDefault(t *testing.T) {
	catalog := Catalog{Providers: []Provider{{
		ID:              "odd",
		Auth:            []string{"api_key"},
		CredentialEnv:   []string{"ODD_KEY"},
		CredentialOrder: []string{"nonsense"},
	}}}

	got := CredentialPrecedence(catalog, "odd")
	if len(got) != 2 || got[0] != SourceEnvironment || got[1] != SourceStored {
		t.Errorf("precedence = %v, want the [environment stored] default: a list naming neither source says nothing", got)
	}
}

func TestAnUnknownProviderHasNoCredentialAndNoPrecedence(t *testing.T) {
	catalog := realCatalog(t)
	if _, ok := LookupCredential(catalog, nil, storedKey("from-a-login"), "not-a-provider"); ok {
		t.Error("an unknown provider must not resolve a credential")
	}
	got := CredentialPrecedence(catalog, "not-a-provider")
	if len(got) != 2 || got[0] != SourceEnvironment || got[1] != SourceStored {
		t.Errorf("precedence = %v, want the [environment stored] default", got)
	}
}

func TestAnEmptyEnvironmentValueIsNotACredential(t *testing.T) {
	catalog := realCatalog(t)
	held := []EnvironmentValue{{Name: "KIMI_API_KEY", Value: ""}}
	if credential, ok := LookupCredential(catalog, held, nil, "kimi"); ok {
		t.Errorf("credential = %+v, want none: a variable that is set to nothing is not a key", credential)
	}
	if credential, ok := LookupCredential(catalog, held, storedKey("from-a-login"), "kimi"); !ok {
		t.Error("kimi still finds the stored key it prefers when the environment holds an empty value")
	} else if credential.Source != SourceStored {
		t.Errorf("credential = %+v, want the stored one", credential)
	}
}
