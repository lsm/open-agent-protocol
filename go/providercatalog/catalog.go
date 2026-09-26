package providercatalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	SchemaFile  = "catalog.schema.json"
	CatalogFile = "catalog.json"
)

const schemaURL = "https://open-agent-protocol.local/providers/" + SchemaFile

type Catalog struct {
	Providers []Provider
}

type Provider struct {
	ID            string       `json:"id"`
	DisplayName   string       `json:"display_name,omitempty"`
	Auth          []string     `json:"auth,omitempty"`
	Offering      string       `json:"offering,omitempty"`
	Status        string       `json:"status,omitempty"`
	CredentialEnv []string     `json:"credential_env,omitempty"`
	BaseURLEnv    []string     `json:"base_url_env,omitempty"`
	RegionEnv     string       `json:"region_env,omitempty"`
	Wires         []string     `json:"wires,omitempty"`
	BaseURLSource string       `json:"base_url_source,omitempty"`
	Endpoints     []Endpoint   `json:"endpoints,omitempty"`
	ModelsPath    string       `json:"models_endpoint,omitempty"`
	OAuthOrigin   *OAuthOrigin `json:"oauth_origin,omitempty"`
	Docs          string       `json:"docs,omitempty"`
}

type Endpoint struct {
	Wire    string `json:"wire"`
	BaseURL string `json:"base_url"`
	Region  string `json:"region,omitempty"`
}

type OAuthOrigin struct {
	Exact                    []string `json:"exact,omitempty"`
	Domain                   string   `json:"domain,omitempty"`
	CredentialDeclaresOrigin bool     `json:"credential_declares_origin,omitempty"`
}

func Load(files fs.FS) (Catalog, error) {
	schema, err := CompileSchema(files)
	if err != nil {
		return Catalog{}, err
	}
	data, err := fs.ReadFile(files, CatalogFile)
	if err != nil {
		return Catalog{}, fmt.Errorf("read %s: %w", CatalogFile, err)
	}
	catalog, err := Parse(schema, CatalogFile, data)
	if err != nil {
		return Catalog{}, err
	}
	if len(catalog.Providers) == 0 {
		return Catalog{}, errors.New("the provider catalog holds no provider")
	}
	return catalog, nil
}

func CompileSchema(files fs.FS) (*jsonschema.Schema, error) {
	data, err := fs.ReadFile(files, SchemaFile)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", SchemaFile, err)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", SchemaFile, err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource(schemaURL, document); err != nil {
		return nil, fmt.Errorf("register %s: %w", SchemaFile, err)
	}
	schema, err := compiler.Compile(schemaURL)
	if err != nil {
		return nil, fmt.Errorf("compile %s: %w", SchemaFile, err)
	}
	return schema, nil
}

func Parse(schema *jsonschema.Schema, name string, data []byte) (Catalog, error) {
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return Catalog{}, fmt.Errorf("%s: %w", name, err)
	}
	if err := schema.Validate(instance); err != nil {
		return Catalog{}, fmt.Errorf("%s: %w", name, err)
	}
	return DecodeStrict(data)
}

func DecodeStrict(data []byte) (Catalog, error) {
	if key, repeated := repeatedMember(data); repeated {
		return Catalog{}, fmt.Errorf("member %q appears twice in one object", key)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var catalog Catalog
	if err := decoder.Decode(&catalog); err != nil {
		return Catalog{}, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Catalog{}, errors.New("data after the catalog object")
	}
	return catalog, nil
}

func repeatedMember(data []byte) (string, bool) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() (string, bool)
	walk = func() (string, bool) {
		token, err := decoder.Token()
		if err != nil {
			return "", false
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return "", false
		}
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return "", false
				}
				key, ok := keyToken.(string)
				if !ok {
					return "", false
				}
				if _, repeated := seen[key]; repeated {
					return key, true
				}
				seen[key] = struct{}{}
				if nested, found := walk(); found {
					return nested, true
				}
			}
			if _, err := decoder.Token(); err != nil {
				return "", false
			}
		case '[':
			for decoder.More() {
				if nested, found := walk(); found {
					return nested, true
				}
			}
			if _, err := decoder.Token(); err != nil {
				return "", false
			}
		}
		return "", false
	}
	return walk()
}
