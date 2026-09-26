package providercatalog

import "strings"

type wirePath struct {
	suffix       string
	trim         bool
	dedupVersion bool
	idempotent   bool
	modelScoped  bool
}

var wirePaths = map[string]wirePath{
	"openai-completions":     {suffix: "/v1/chat/completions", trim: true, dedupVersion: true, idempotent: true},
	"openai-responses":       {suffix: "/v1/responses"},
	"openai-codex-responses": {suffix: "/responses"},
	"anthropic-messages":     {suffix: "/v1/messages", dedupVersion: true, idempotent: true},
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
	trimmed := base
	if path.trim {
		trimmed = strings.TrimRight(base, "/")
	}
	if path.idempotent && strings.HasSuffix(trimmed, path.suffix) {
		return trimmed
	}
	suffix := path.suffix
	if path.dedupVersion && pathHasVersion(trimmed) && strings.HasPrefix(suffix, "/v1/") {
		suffix = strings.TrimPrefix(suffix, "/v1")
	}
	return trimmed + suffix
}

func joinModels(base, path string) string {
	return base + path
}

func findProvider(catalog Catalog, id string) (Provider, bool) {
	for _, provider := range catalog.Providers {
		if provider.ID == id {
			return provider, true
		}
	}
	return Provider{}, false
}

func ModelsURL(catalog Catalog, id, region string) string {
	provider, known := findProvider(catalog, id)
	if !known || provider.ModelsPath == "" {
		return ""
	}
	for _, endpoint := range provider.Endpoints {
		if endpoint.Region != region {
			continue
		}
		return joinModels(endpoint.BaseURL, provider.ModelsPath)
	}
	return ""
}

func RequestURL(catalog Catalog, id, wire, region string) string {
	provider, known := findProvider(catalog, id)
	if !known {
		return ""
	}
	path, joined := wirePaths[wire]
	if !joined || path.modelScoped {
		return ""
	}
	for _, endpoint := range provider.Endpoints {
		if endpoint.Wire != wire || endpoint.Region != region {
			continue
		}
		return joinRequest(endpoint.BaseURL, path)
	}
	return ""
}

func Resolve(catalog Catalog) []Resolved {
	var resolved []Resolved
	for _, provider := range catalog.Providers {
		for _, endpoint := range provider.Endpoints {
			resolved = append(resolved, Resolved{
				ID:         provider.ID,
				Wire:       endpoint.Wire,
				Region:     endpoint.Region,
				BaseURL:    endpoint.BaseURL,
				ModelsURL:  ModelsURL(catalog, provider.ID, endpoint.Region),
				RequestURL: RequestURL(catalog, provider.ID, endpoint.Wire, endpoint.Region),
			})
		}
	}
	return resolved
}
