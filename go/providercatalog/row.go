package providercatalog

import "strings"

func Status(catalog Catalog, id string) string {
	provider, known := findProvider(catalog, id)
	if !known {
		return "supported"
	}
	if provider.Status == "" {
		return "supported"
	}
	return provider.Status
}

func Offering(catalog Catalog, id string) string {
	provider, known := findProvider(catalog, id)
	if !known {
		return ""
	}
	return provider.Offering
}

func CredentialEnv(catalog Catalog, id string) []string {
	provider, known := findProvider(catalog, id)
	if !known {
		return nil
	}
	return provider.CredentialEnv
}

func BaseURLEnv(catalog Catalog, id string) []string {
	provider, known := findProvider(catalog, id)
	if !known {
		return nil
	}
	return provider.BaseURLEnv
}

func DefaultRegion(catalog Catalog, id string) string {
	provider, known := findProvider(catalog, id)
	if !known {
		return ""
	}
	return provider.DefaultRegion
}

func RegionEnv(catalog Catalog, id string) string {
	provider, known := findProvider(catalog, id)
	if !known {
		return ""
	}
	return provider.RegionEnv
}

func ModelsEndpoint(catalog Catalog, id string) string {
	provider, known := findProvider(catalog, id)
	if !known {
		return ""
	}
	return provider.ModelsPath
}

func OAuthOriginFor(catalog Catalog, id string) (OAuthOrigin, bool) {
	provider, known := findProvider(catalog, id)
	if !known || provider.OAuthOrigin == nil {
		return OAuthOrigin{}, false
	}
	return *provider.OAuthOrigin, true
}

func Wires(catalog Catalog, id string) []string {
	provider, known := findProvider(catalog, id)
	if !known {
		return nil
	}
	return provider.Wires
}

func Endpoints(catalog Catalog, id string) []Endpoint {
	provider, known := findProvider(catalog, id)
	if !known {
		return nil
	}
	return provider.Endpoints
}

func servesRegion(endpoint Endpoint, region string) bool {
	if region != "" {
		return endpoint.Region == region
	}
	return endpoint.Region == ""
}

func BaseURL(catalog Catalog, id, wire, region string) (string, bool) {
	provider, known := findProvider(catalog, id)
	if !known {
		return "", false
	}
	for _, endpoint := range provider.Endpoints {
		if endpoint.Wire != wire || !servesRegion(endpoint, region) {
			continue
		}
		return endpoint.BaseURL, true
	}
	return "", false
}

func DefaultBaseURL(catalog Catalog, id, region string) (string, bool) {
	provider, known := findProvider(catalog, id)
	if !known {
		return "", false
	}
	if provider.BaseURLSource == "" {
		return "", false
	}
	for _, endpoint := range provider.Endpoints {
		if !servesRegion(endpoint, region) {
			continue
		}
		return endpoint.BaseURL, true
	}
	return "", false
}

var responsesOnlyPrefixes = []string{
	"o1-pro",
	"o3-pro",
	"gpt-5-pro",
	"gpt-5-codex",
	"gpt-5.1-codex-max",
	"computer-use-preview",
}

func IsResponsesOnlyModel(modelID string) bool {
	for _, prefix := range responsesOnlyPrefixes {
		if strings.HasPrefix(modelID, prefix) {
			return true
		}
	}
	return strings.Contains(modelID, "deep-research")
}

func FirstImplementedWire(row Provider) string {
	for _, wire := range row.Wires {
		if _, known := wirePaths[wire]; known {
			return wire
		}
	}
	return ""
}

func WireForModel(catalog Catalog, id, modelID string) string {
	provider, known := findProvider(catalog, id)
	if !known {
		return ""
	}
	if id == "openai" && IsResponsesOnlyModel(modelID) {
		if _, joined := wirePaths["openai-responses"]; joined {
			return "openai-responses"
		}
	}
	return FirstImplementedWire(provider)
}
