package providercatalog

type Source string

const (
	SourceNone        Source = "none"
	SourceEnvironment Source = "environment"
	SourceStored      Source = "stored"
	SourceOAuth       Source = "oauth"
)

type Credential struct {
	Key    string
	Source Source
	Name   string
}

type EnvironmentValue struct {
	Name  string
	Value string
}

type StoredCredential struct {
	APIKey      *string
	OAuthAccess *string
}

func APIKeyFromNames(env []EnvironmentValue, names []string) (Credential, bool) {
	for _, name := range names {
		for _, held := range env {
			if held.Name != name {
				continue
			}
			if held.Value == "" {
				break
			}
			return Credential{Key: held.Value, Source: SourceEnvironment, Name: name}, true
		}
	}
	return Credential{}, false
}

func APIKeyForProvider(catalog Catalog, env []EnvironmentValue, id string) (Credential, bool) {
	return APIKeyFromNames(env, CredentialEnv(catalog, id))
}

func AcceptsAuth(catalog Catalog, id, kind string) bool {
	provider, known := findProvider(catalog, id)
	if !known {
		return false
	}
	for _, accepted := range provider.Auth {
		if accepted == kind {
			return true
		}
	}
	return false
}

func NeedsNoCredential(catalog Catalog, id string) bool {
	return AcceptsAuth(catalog, id, "none")
}

func CredentialPrecedence(catalog Catalog, id string) []Source {
	provider, known := findProvider(catalog, id)
	if !known {
		return nil
	}
	return precedenceOrder(provider.CredentialOrder)
}

func precedenceOrder(names []string) []Source {
	out := make([]Source, 0, 2)
	for _, name := range names {
		if len(out) == cap(out) {
			break
		}
		if name == string(SourceStored) {
			out = append(out, SourceStored)
			continue
		}
		if name == string(SourceEnvironment) {
			out = append(out, SourceEnvironment)
		}
	}
	if len(out) == 0 {
		return []Source{SourceEnvironment, SourceStored}
	}
	return out
}

func LookupCredential(catalog Catalog, env []EnvironmentValue, stored *StoredCredential, id string) (Credential, bool) {
	provider, known := findProvider(catalog, id)
	if !known {
		return Credential{}, false
	}
	for _, source := range precedenceOrder(provider.CredentialOrder) {
		switch source {
		case SourceEnvironment:
			if credential, ok := APIKeyForProvider(catalog, env, id); ok {
				return credential, true
			}
		case SourceStored:
			if credential, ok := storedCredential(catalog, stored, id); ok {
				return credential, true
			}
		}
	}
	return Credential{}, false
}

func storedCredential(catalog Catalog, stored *StoredCredential, id string) (Credential, bool) {
	if stored == nil {
		return Credential{}, false
	}
	if stored.APIKey != nil {
		if !AcceptsAuth(catalog, id, "api_key") {
			return Credential{}, false
		}
		if *stored.APIKey == "" {
			return Credential{}, false
		}
		return Credential{Key: *stored.APIKey, Source: SourceStored, Name: id}, true
	}
	if stored.OAuthAccess != nil {
		if !AcceptsAuth(catalog, id, "oauth") {
			return Credential{}, false
		}
		if *stored.OAuthAccess == "" {
			return Credential{}, false
		}
		return Credential{Key: *stored.OAuthAccess, Source: SourceOAuth, Name: id}, true
	}
	return Credential{}, false
}
