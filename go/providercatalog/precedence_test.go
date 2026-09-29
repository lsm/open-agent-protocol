package providercatalog

import "testing"

func keyOf(value string) *string { return &value }

func envHolding(pairs ...string) []EnvironmentValue {
	held := make([]EnvironmentValue, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		held = append(held, EnvironmentValue{Name: pairs[i], Value: pairs[i+1]})
	}
	return held
}

func TestTheRealCatalogNamesStoredFirstForKimiAndNothingForTheRest(t *testing.T) {
	catalog := heldCatalog(t)
	if got := CredentialPrecedence(catalog, "kimi"); len(got) != 2 || got[0] != SourceStored || got[1] != SourceEnvironment {
		t.Errorf("kimi = %v, want stored before environment: the row says so and the two sources disagree about the region", got)
	}
	named := 0
	for _, provider := range catalog.Providers {
		if len(provider.CredentialOrder) > 0 {
			named++
		}
	}
	if named != 1 {
		t.Errorf("%d rows name a precedence, want 1: a test over the real catalog has to notice when that changes", named)
	}
}

func TestAStoredLoginOutranksTheVariableForKimi(t *testing.T) {
	catalog := heldCatalog(t)
	credential, ok := LookupCredential(catalog, envHolding("KIMI_API_KEY", "from-the-environment"), &StoredCredential{APIKey: keyOf("from-the-login")}, "kimi")
	if !ok {
		t.Fatal("kimi with both held no credential")
	}
	if credential.Key != "from-the-login" || credential.Source != SourceStored {
		t.Errorf("kimi = %+v, want the stored key: signing with the variable's would present it to the other region", credential)
	}
}

func TestARowWithoutTheFieldStillTakesTheEnvironmentFirst(t *testing.T) {
	catalog := heldCatalog(t)
	credential, ok := LookupCredential(catalog, envHolding("OPENAI_API_KEY", "from-the-environment"), &StoredCredential{APIKey: keyOf("from-the-login")}, "openai")
	if !ok {
		t.Fatal("openai with both held no credential")
	}
	if credential.Key != "from-the-environment" || credential.Source != SourceEnvironment {
		t.Errorf("openai = %+v, want the variable: absent means environment then stored", credential)
	}
}

func TestKimiFallsThroughToTheVariableWhenNoLoginIsHeld(t *testing.T) {
	catalog := heldCatalog(t)
	credential, ok := LookupCredential(catalog, envHolding("KIMI_API_KEY", "from-the-environment"), nil, "kimi")
	if !ok || credential.Source != SourceEnvironment || credential.Key != "from-the-environment" {
		t.Errorf("kimi with only the variable = %+v, ok=%v, want the variable: a source that yields nothing falls through", credential, ok)
	}
}

func TestTheDeclaredOrderIsFollowedEvenWhenItsFirstSourceIsUnusable(t *testing.T) {
	catalog := heldCatalog(t)
	credential, ok := LookupCredential(catalog, envHolding("KIMI_API_KEY", "from-the-environment"), &StoredCredential{APIKey: keyOf("")}, "kimi")
	if !ok || credential.Source != SourceEnvironment {
		t.Errorf("kimi with a stored key that is set but empty = %+v, ok=%v, want the variable: stored comes first but yields nothing", credential, ok)
	}
}

func TestAStoredKeyARowDoesNotAcceptIsSteppedOverRatherThanReturned(t *testing.T) {
	shape := Catalog{Providers: []Provider{{
		ID:              "oauth-only",
		Auth:            []string{"oauth"},
		CredentialEnv:   []string{"OAUTH_ONLY_KEY"},
		CredentialOrder: []string{"stored", "environment"},
	}}}
	credential, ok := LookupCredential(shape, envHolding("OAUTH_ONLY_KEY", "from-the-environment"), &StoredCredential{APIKey: keyOf("never-usable")}, "oauth-only")
	if !ok || credential.Source != SourceEnvironment {
		t.Errorf("got %+v, ok=%v, want the variable: the row takes oauth, so its stored key is not a credential for it", credential, ok)
	}
}

func TestAListNamingOneSourceConsultsOnlyThatOne(t *testing.T) {
	if got := precedenceOrder([]string{"stored"}); len(got) != 1 || got[0] != SourceStored {
		t.Errorf("got %v, want stored alone: the variable must not be consulted for a row that named only the login", got)
	}
	if got := precedenceOrder([]string{"environment"}); len(got) != 1 || got[0] != SourceEnvironment {
		t.Errorf("got %v, want environment alone", got)
	}
}

func TestAListOfNamesThatAreNotSourcesFallsBackToTheDefaultPair(t *testing.T) {
	for _, names := range [][]string{nil, {}, {"credential_env"}, {"Stored", "ENVIRONMENT"}, {"oauth", "none"}} {
		got := precedenceOrder(names)
		if len(got) != 2 || got[0] != SourceEnvironment || got[1] != SourceStored {
			t.Errorf("precedenceOrder(%v) = %v, want the default pair: an unrecognised list is not a refusal", names, got)
		}
	}
}

func TestCollectionStopsAtTwoSources(t *testing.T) {
	got := precedenceOrder([]string{"stored", "stored", "environment"})
	if len(got) != 2 || got[0] != SourceStored || got[1] != SourceStored {
		t.Errorf("got %v, want two stored: a third name is never consulted", got)
	}
	if got := precedenceOrder([]string{"stored", "environment", "stored"}); len(got) != 2 || got[0] != SourceStored || got[1] != SourceEnvironment {
		t.Errorf("got %v, want the first two and no third", got)
	}
}

func TestAStoredOAuthTokenStillAnswersOAuthAndStillRespectsAuth(t *testing.T) {
	catalog := heldCatalog(t)
	credential, ok := LookupCredential(catalog, nil, &StoredCredential{OAuthAccess: keyOf("a-token")}, "openai-codex")
	if !ok || credential.Source != SourceOAuth || credential.Key != "a-token" {
		t.Errorf("openai-codex with a stored token = %+v, ok=%v, want the token as oauth", credential, ok)
	}
	if _, ok := LookupCredential(catalog, nil, &StoredCredential{OAuthAccess: keyOf("a-token")}, "openai"); ok {
		t.Error("openai lists only api_key, so a stored token must not answer for it")
	}
}

func TestAnUnknownRowAnswersNoCredentialWhateverItHolds(t *testing.T) {
	catalog := heldCatalog(t)
	if got := CredentialPrecedence(catalog, "a-row-that-is-not-there"); got != nil {
		t.Errorf("got %v, want nothing for an unknown row", got)
	}
	if _, ok := LookupCredential(catalog, envHolding("ANYTHING", "x"), &StoredCredential{APIKey: keyOf("y")}, "a-row-that-is-not-there"); ok {
		t.Error("an unknown row must answer no credential even with both sources held")
	}
}
