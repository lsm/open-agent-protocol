package provider

import "errors"

var ErrMissingAPIKey = errors.New("missing api key")

type Credential struct {
	Key    string
	Source string
	Name   string
}

const (
	SourceCaller      = "caller"
	SourceEnvironment = "environment"
	SourceAnonymous   = "anonymous"
)

type CredentialEnv func(providerID string) (Credential, bool)

type KeyOptions struct {
	Key    string
	Source string
	Name   string
}

func ResolveAPIKey(model Model, caller KeyOptions, fromEnv CredentialEnv) (string, error) {
	if caller.Key != "" {
		return caller.Key, nil
	}
	if fromEnv != nil {
		if held, ok := fromEnv(model.Provider); ok {
			if held.Key != "" {
				return held.Key, nil
			}
		}
	}
	if AllowsAnonymous(model) {
		return "", nil
	}
	return "", ErrMissingAPIKey
}

func BearerValue(key string) string {
	if key == "" {
		return ""
	}
	return "Bearer " + key
}
