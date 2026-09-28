package providercatalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
)

type wirePath struct {
	suffix      string
	modelScoped bool
}

var wirePaths = map[string]wirePath{
	"openai-completions":     {suffix: "/v1/chat/completions"},
	"openai-responses":       {suffix: "/v1/responses"},
	"openai-codex-responses": {suffix: "/responses"},
	"anthropic-messages":     {suffix: "/v1/messages"},
	"ollama":                 {suffix: "/api/chat"},
	"google-generative-ai":   {modelScoped: true},
}

type Resolved struct {
	ID         string
	Wire       string
	Region     string
	BaseURL    string
	ModelsURL  string
	RequestURL string
}

func CarriesVersion(catalog Catalog, id, baseURL string) bool {
	provider, known := findProvider(catalog, id)
	if !known {
		return false
	}
	wanted := strings.TrimRight(baseURL, "/")
	for _, endpoint := range provider.Endpoints {
		if strings.TrimRight(endpoint.BaseURL, "/") != wanted {
			continue
		}
		return endpoint.CarriesVersion
	}
	return false
}

func CarriesVersionFor(catalog Catalog, id, baseURL string, stated *bool) bool {
	if stated != nil {
		return *stated
	}
	return CarriesVersion(catalog, id, baseURL)
}

func joinRequest(base string, path wirePath, carriesVersion bool) string {
	trimmed := strings.TrimRight(base, "/")
	if strings.HasSuffix(trimmed, path.suffix) {
		return trimmed
	}
	suffix := path.suffix
	if carriesVersion && strings.HasPrefix(suffix, "/v1/") {
		suffix = strings.TrimPrefix(suffix, "/v1")
	}
	return trimmed + suffix
}

func joinModels(base, path string, carriesVersion bool) string {
	return joinRequest(base, wirePath{suffix: path}, carriesVersion)
}

func listingPath(path string, carriesVersion, overridden bool) string {
	if carriesVersion && overridden {
		return "/v1" + path
	}
	return path
}

func wireTakesVersionedPath(wire string) bool {
	path, joined := wirePaths[wire]
	return joined && strings.HasPrefix(path.suffix, "/v1/")
}

func findProvider(catalog Catalog, id string) (Provider, bool) {
	for _, provider := range catalog.Providers {
		if provider.ID == id {
			return provider, true
		}
	}
	return Provider{}, false
}

func ModelsURL(catalog Catalog, id, region string) (string, bool) {
	provider, known := findProvider(catalog, id)
	if !known || provider.ModelsPath == "" {
		return "", false
	}
	for _, endpoint := range provider.Endpoints {
		if endpoint.Region != region {
			continue
		}
		return joinModels(endpoint.BaseURL, provider.ModelsPath, endpoint.CarriesVersion), true
	}
	return "", false
}

func ModelsURLForBase(baseURL, path string, carriesVersion, overridden bool) string {
	return joinModels(baseURL, listingPath(path, carriesVersion, overridden), carriesVersion && !overridden)
}

func RequestURL(catalog Catalog, id, wire, region string) (string, bool) {
	provider, known := findProvider(catalog, id)
	if !known {
		return "", false
	}
	path, joined := wirePaths[wire]
	if !joined || path.modelScoped {
		return "", false
	}
	for _, endpoint := range provider.Endpoints {
		if endpoint.Wire != wire || endpoint.Region != region {
			continue
		}
		return joinRequest(endpoint.BaseURL, path, endpoint.CarriesVersion), true
	}
	return "", false
}

func RequestURLForBase(baseURL string, path wirePath, carriesVersion bool) string {
	return joinRequest(baseURL, path, carriesVersion)
}

func Resolve(catalog Catalog) []Resolved {
	var resolved []Resolved
	for _, provider := range catalog.Providers {
		for _, endpoint := range provider.Endpoints {
			models := ""
			if provider.ModelsPath != "" {
				models = joinModels(endpoint.BaseURL, provider.ModelsPath, endpoint.CarriesVersion)
			}
			request := ""
			if path, joined := wirePaths[endpoint.Wire]; joined && !path.modelScoped {
				request = joinRequest(endpoint.BaseURL, path, endpoint.CarriesVersion)
			}
			resolved = append(resolved, Resolved{
				ID:         provider.ID,
				Wire:       endpoint.Wire,
				Region:     endpoint.Region,
				BaseURL:    endpoint.BaseURL,
				ModelsURL:  models,
				RequestURL: request,
			})
		}
	}
	return resolved
}

const PinnedFile = "resolved_urls.json"

type Pinned struct {
	ID         string `json:"id"`
	Wire       string `json:"wire"`
	Region     string `json:"region,omitempty"`
	BaseURL    string `json:"base_url"`
	ModelsURL  string `json:"models_url,omitempty"`
	RequestURL string `json:"request_url,omitempty"`
}

type PinnedCatalog struct {
	Endpoints []Pinned `json:"endpoints"`
}

func PinnedFrom(catalog Catalog) PinnedCatalog {
	resolved := Resolve(catalog)
	pinned := PinnedCatalog{Endpoints: make([]Pinned, 0, len(resolved))}
	for _, row := range resolved {
		pinned.Endpoints = append(pinned.Endpoints, Pinned{
			ID:         row.ID,
			Wire:       row.Wire,
			Region:     row.Region,
			BaseURL:    row.BaseURL,
			ModelsURL:  row.ModelsURL,
			RequestURL: row.RequestURL,
		})
	}
	return pinned
}

func LoadPinned(files fs.FS) (PinnedCatalog, error) {
	data, err := fs.ReadFile(files, PinnedFile)
	if err != nil {
		return PinnedCatalog{}, fmt.Errorf("read %s: %w", PinnedFile, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var pinned PinnedCatalog
	if err := decoder.Decode(&pinned); err != nil {
		return PinnedCatalog{}, fmt.Errorf("%s: %w", PinnedFile, err)
	}
	if len(pinned.Endpoints) == 0 {
		return PinnedCatalog{}, fmt.Errorf("%s pins no endpoint", PinnedFile)
	}
	return pinned, nil
}

func EncodePinned(catalog Catalog) ([]byte, error) {
	return json.MarshalIndent(PinnedFrom(catalog), "", "  ")
}

func CheckPinned(catalog Catalog, pinned PinnedCatalog) []Finding {
	resolved := PinnedFrom(catalog)
	if len(pinned.Endpoints) != len(resolved.Endpoints) {
		return []Finding{{
			Code: CodeStaleURLs,
			Detail: fmt.Sprintf("%s pins %d endpoints, the catalog resolves %d; run goap providers catalog-urls --format=json > providers/%s",
				PinnedFile, len(pinned.Endpoints), len(resolved.Endpoints), PinnedFile),
		}}
	}
	for index, want := range resolved.Endpoints {
		if pinned.Endpoints[index] == want {
			continue
		}
		return []Finding{{
			Provider: want.ID,
			Code:     CodeStaleURLs,
			Detail: fmt.Sprintf("%s pins %s on %s in %s as %+v, the catalog resolves %+v; run goap providers catalog-urls --format=json > providers/%s",
				PinnedFile, want.ID, want.Wire, regionOrDefault(want.Region), pinned.Endpoints[index], want, PinnedFile),
		}}
	}
	return nil
}

func regionOrDefault(region string) string {
	if region == "" {
		return "the default region"
	}
	return region
}
