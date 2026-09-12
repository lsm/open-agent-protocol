// Package serve exposes the OAP adapter registry over a single-user local
// HTTP + SSE daemon. The daemon is a thin translation of adapter.Session onto
// HTTP: request and event bodies are verbatim schema/v0.1 envelopes, and the
// daemon adds no protocol semantics of its own.
package serve

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/acp"
	"github.com/lsm/open-agent-protocol/adapter/claude"
	"github.com/lsm/open-agent-protocol/adapter/codex/appserver"
	"github.com/lsm/open-agent-protocol/adapter/deepseek"
	"github.com/lsm/open-agent-protocol/adapter/hermes"
	"github.com/lsm/open-agent-protocol/adapter/makai"
	"github.com/lsm/open-agent-protocol/adapter/opencode"
	"github.com/lsm/open-agent-protocol/adapter/pi"
)

// configFile is the oap serve registry document: named adapter entries, each
// mapping onto one in-repo adapter configuration.
type configFile struct {
	Adapters map[string]adapterEntry `json:"adapters"`
}

// adapterEntry carries the fields the daemon can express for the in-repo
// adapters. Per-type requirements are enforced by the adapters' own
// constructors, so a registry entry that omits a required field fails at load
// time with the adapter's own diagnostic.
type adapterEntry struct {
	Type             string   `json:"type"`
	Executable       string   `json:"executable"`
	Args             []string `json:"args"`
	Environment      []string `json:"environment"`
	WorkingDirectory string   `json:"working_directory"`
	Model            string   `json:"model"`
	JournalCapacity  int      `json:"journal_capacity"`

	// Codex app-server.
	ApprovalPolicy string `json:"approval_policy"`
	Sandbox        string `json:"sandbox"`

	// DeepSeek Harness.
	Provider  string `json:"provider"`
	MaxTokens *int64 `json:"max_tokens"`

	// Makai.
	AgentConfig  json.RawMessage `json:"agent_config"`
	SystemPrompt string          `json:"system_prompt"`

	// OpenCode (HTTP server, no child process).
	Endpoint string `json:"endpoint"`
	Agent    string `json:"agent"`
}

// Registry maps adapter names to implementations. It is immutable once built.
type Registry struct {
	adapters map[string]base.Adapter
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]base.Adapter)}
}

// DefaultRegistry returns the built-in registry used when no config file is
// given: the deterministic memory reference adapter only.
func DefaultRegistry() (*Registry, error) {
	registry := NewRegistry()
	if err := registry.Register("memory", base.NewMemory(base.Config{})); err != nil {
		return nil, err
	}
	return registry, nil
}

// Register adds one named adapter implementation; programmatic entries use the
// same surface as config-file entries.
func (r *Registry) Register(name string, implementation base.Adapter) error {
	if name == "" {
		return errors.New("serve: adapter name is required")
	}
	if _, exists := r.adapters[name]; exists {
		return fmt.Errorf("serve: adapter %q is already registered", name)
	}
	r.adapters[name] = implementation
	return nil
}

// Lookup returns the implementation registered under name.
func (r *Registry) Lookup(name string) (base.Adapter, bool) {
	implementation, ok := r.adapters[name]
	return implementation, ok
}

// Names returns the registered adapter names in stable order.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.adapters))
	for name := range r.adapters {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *Registry) adaptersLen() int { return len(r.adapters) }

// LoadRegistry reads a registry document and constructs every entry. Adapter
// construction is eager, so constructor requirements (absolute working
// directories, executables, provider settings) surface at daemon start rather
// than at first use. Environ resolves bare allowlist names against the
// daemon's own environment; pass os.LookupEnv outside tests.
func LoadRegistry(path string, environ func(string) (string, bool)) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("serve: read config: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var file configFile
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("serve: parse config %s: %w", path, err)
	}
	if decoder.More() {
		return nil, fmt.Errorf("serve: parse config %s: trailing data after the registry object", path)
	}
	registry := NewRegistry()
	for _, name := range sortedKeys(file.Adapters) {
		implementation, err := buildAdapter(name, file.Adapters[name], environ)
		if err != nil {
			return nil, err
		}
		if err := registry.Register(name, implementation); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func sortedKeys[K ~string, V any](mapping map[K]V) []string {
	keys := make([]string, 0, len(mapping))
	for key := range mapping {
		keys = append(keys, string(key))
	}
	sort.Strings(keys)
	return keys
}

func buildAdapter(name string, entry adapterEntry, environ func(string) (string, bool)) (base.Adapter, error) {
	kind := entry.Type
	if kind == "" {
		kind = name
	}
	environment, err := resolveEnvironment(entry.Environment, environ)
	if err != nil {
		return nil, fmt.Errorf("serve: adapter %q: %w", name, err)
	}
	switch kind {
	case "memory":
		return base.NewMemory(base.Config{JournalCapacity: entry.JournalCapacity}), nil
	case "claude":
		implementation, err := claude.New(claude.Config{
			Executable: entry.Executable, Args: entry.Args, Environment: environment,
			WorkingDirectory: entry.WorkingDirectory, Model: entry.Model, JournalCapacity: entry.JournalCapacity,
		})
		return implementation, wrapBuild(name, err)
	case "codex":
		implementation, err := appserver.New(appserver.Config{
			Executable: entry.Executable, Args: entry.Args, Environment: environment,
			WorkingDirectory: entry.WorkingDirectory, Model: entry.Model, ApprovalPolicy: entry.ApprovalPolicy,
			Sandbox: entry.Sandbox, JournalCapacity: entry.JournalCapacity,
		})
		return implementation, wrapBuild(name, err)
	case "acp":
		implementation, err := acp.New(acp.Config{
			Executable: entry.Executable, Args: entry.Args, Environment: environment,
			WorkingDirectory: entry.WorkingDirectory, JournalCapacity: entry.JournalCapacity,
		})
		return implementation, wrapBuild(name, err)
	case "makai":
		implementation, err := makai.New(makai.Config{
			Executable: entry.Executable, Args: entry.Args, Environment: environment,
			WorkingDirectory: entry.WorkingDirectory, AgentConfig: entry.AgentConfig,
			SystemPrompt: entry.SystemPrompt, JournalCapacity: entry.JournalCapacity,
		})
		return implementation, wrapBuild(name, err)
	case "opencode":
		implementation, err := opencode.New(opencode.Config{
			Endpoint: entry.Endpoint, Agent: entry.Agent, JournalCapacity: entry.JournalCapacity,
		})
		return implementation, wrapBuild(name, err)
	case "pi":
		implementation, err := pi.New(pi.Config{
			Executable: entry.Executable, Args: entry.Args, Environment: environment,
			WorkingDirectory: entry.WorkingDirectory, JournalCapacity: entry.JournalCapacity,
		})
		return implementation, wrapBuild(name, err)
	case "deepseek":
		implementation, err := deepseek.New(deepseek.Config{
			Executable: entry.Executable, Args: entry.Args, Environment: environment,
			WorkingDirectory: entry.WorkingDirectory, Provider: entry.Provider, Model: entry.Model,
			MaxTokens: entry.MaxTokens, JournalCapacity: entry.JournalCapacity,
		})
		return implementation, wrapBuild(name, err)
	case "hermes":
		implementation, err := hermes.New(hermes.Config{
			Executable: entry.Executable, Args: entry.Args, Environment: environment,
			WorkingDirectory: entry.WorkingDirectory, Model: entry.Model, JournalCapacity: entry.JournalCapacity,
		})
		return implementation, wrapBuild(name, err)
	default:
		return nil, fmt.Errorf("serve: adapter %q: unknown type %q", name, kind)
	}
}

func wrapBuild(name string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("serve: adapter %q: %w", name, err)
}

// resolveEnvironment turns the config allowlist into the verbatim child
// environment: a bare NAME forwards the value the daemon itself carries and is
// omitted entirely when unset, while NAME=value passes through literally. The
// result is never nil, so adapters that treat a nil environment as "inherit
// ambient" stay on an explicit allowlist and no ambient credential can reach a
// child process unless its variable was listed here.
func resolveEnvironment(entries []string, environ func(string) (string, bool)) ([]string, error) {
	resolved := make([]string, 0, len(entries))
	for _, entry := range entries {
		name, _, literal := strings.Cut(entry, "=")
		if name == "" {
			return nil, errors.New("environment entry has no variable name")
		}
		if literal {
			resolved = append(resolved, entry)
			continue
		}
		if value, ok := environ(name); ok {
			resolved = append(resolved, name+"="+value)
		}
	}
	return resolved, nil
}
