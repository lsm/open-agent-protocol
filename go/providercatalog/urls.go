package providercatalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
)

type wirePath struct {
	suffix       string
	dedupVersion bool
	modelScoped  bool
}

var wirePaths = map[string]wirePath{
	"openai-completions":     {suffix: "/v1/chat/completions", dedupVersion: true},
	"openai-responses":       {suffix: "/v1/responses"},
	"openai-codex-responses": {suffix: "/responses"},
	"anthropic-messages":     {suffix: "/v1/messages", dedupVersion: true},
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

func isVersionSegment(segment string) bool {
	if len(segment) < 2 || segment[0] != 'v' {
		return false
	}
	for _, digit := range segment[1:] {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func pathHasVersion(baseURL string) bool {
	_, rest, scheme := strings.Cut(baseURL, "://")
	if !scheme {
		return false
	}
	_, path, rooted := strings.Cut(rest, "/")
	if !rooted {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if isVersionSegment(segment) {
			return true
		}
	}
	return false
}

func joinRequest(base string, path wirePath) string {
	trimmed := strings.TrimRight(base, "/")
	if strings.HasSuffix(trimmed, path.suffix) {
		return trimmed
	}
	suffix := path.suffix
	if path.dedupVersion && pathHasVersion(trimmed) && strings.HasPrefix(suffix, "/v1/") {
		suffix = strings.TrimPrefix(suffix, "/v1")
	}
	return trimmed + suffix
}

func joinModels(base, path string) string {
	return joinRequest(base, wirePath{suffix: path})
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
		return joinModels(endpoint.BaseURL, provider.ModelsPath), true
	}
	return "", false
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
		return joinRequest(endpoint.BaseURL, path), true
	}
	return "", false
}

func Resolve(catalog Catalog) []Resolved {
	var resolved []Resolved
	for _, provider := range catalog.Providers {
		for _, endpoint := range provider.Endpoints {
			models := ""
			if provider.ModelsPath != "" {
				models = joinModels(endpoint.BaseURL, provider.ModelsPath)
			}
			request := ""
			if path, joined := wirePaths[endpoint.Wire]; joined && !path.modelScoped {
				request = joinRequest(endpoint.BaseURL, path)
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
