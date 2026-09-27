package publicset

import "strings"

var public = map[string]bool{
	"go/client":              true,
	"go/harness":             true,
	"go/protocol":            true,
	"go/providercatalog":     true,
	"go/serve":               true,
	"go/serve/serveendpoint": true,
	"go/serve/servehttp":     true,
	"go/serve/servestdio":    true,
	"go/validation":          true,
	"harnesses":              true,
	"providers":              true,
	"schema":                 true,
}

func Internal(name string) bool {
	for _, element := range strings.Split(name, "/") {
		if element == "internal" {
			return true
		}
	}
	return false
}

func Public(name string) bool {
	if Internal(name) {
		return false
	}
	if name == "go/adapter" || strings.HasPrefix(name, "go/adapter/") {
		return true
	}
	return public[name]
}
