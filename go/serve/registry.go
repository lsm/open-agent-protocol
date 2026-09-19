package serve

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/acp"
	"github.com/lsm/open-agent-protocol/go/adapter/claude"
	"github.com/lsm/open-agent-protocol/go/adapter/codex/appserver"
	"github.com/lsm/open-agent-protocol/go/adapter/deepseek"
	"github.com/lsm/open-agent-protocol/go/adapter/hermes"
	"github.com/lsm/open-agent-protocol/go/adapter/makai"
	"github.com/lsm/open-agent-protocol/go/adapter/opencode"
	"github.com/lsm/open-agent-protocol/go/adapter/pi"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type configFile struct {
	Adapters map[string]adapterEntry `json:"adapters"`

	ToolSources map[string]toolSourceEntry `json:"tool_sources"`
}

type toolSourceEntry struct {
	Kind        string   `json:"kind"`
	DisplayName string   `json:"display_name"`
	Protocol    string   `json:"protocol"`
	Endpoint    string   `json:"endpoint"`
	Command     string   `json:"command"`
	Args        []string `json:"args"`
	Environment []string `json:"environment"`
}

type adapterEntry struct {
	Type             string   `json:"type"`
	Executable       string   `json:"executable"`
	Args             []string `json:"args"`
	Environment      []string `json:"environment"`
	WorkingDirectory string   `json:"working_directory"`
	Model            string   `json:"model"`
	JournalCapacity  int      `json:"journal_capacity"`

	ApprovalPolicy string `json:"approval_policy"`
	Sandbox        string `json:"sandbox"`

	Provider  string `json:"provider"`
	MaxTokens *int64 `json:"max_tokens"`

	AgentConfig  json.RawMessage `json:"agent_config"`
	SystemPrompt string          `json:"system_prompt"`

	Endpoint string `json:"endpoint"`
	Agent    string `json:"agent"`
}

type Registry struct {
	adapters    map[string]base.Adapter
	toolSources map[string]protocol.ToolSourceAttachment
}

func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]base.Adapter), toolSources: make(map[string]protocol.ToolSourceAttachment)}
}

func (r *Registry) RegisterToolSource(id string, source protocol.ToolSourceAttachment) error {
	if id == "" {
		return errors.New("serve: tool source id is required")
	}
	if err := validateToolSource(id, source.Kind, source.Command, source.Environment); err != nil {
		return err
	}
	if _, exists := r.toolSources[id]; exists {
		return fmt.Errorf("serve: tool source %q is already registered", id)
	}
	source.ID = id
	r.toolSources[id] = source
	return nil
}

func validateToolSource(id, kind, command string, environment []string) error {
	if kind == "" {
		return fmt.Errorf("serve: tool source %q: kind is required", id)
	}
	if !protocol.IsToolSourceKind(kind) {
		return fmt.Errorf("serve: tool source %q: kind %q is not a tool source kind", id, kind)
	}
	if kind == protocol.ToolSourceProcess && command == "" {
		return fmt.Errorf("serve: tool source %q: a process source needs a command", id)
	}

	if name := base.DuplicateEnvironmentName(environment); name != "" {
		return fmt.Errorf("serve: tool source %q: environment names %q twice", id, name)
	}
	return nil
}

func (r *Registry) ToolSource(id string) (protocol.ToolSourceAttachment, bool) {
	source, ok := r.toolSources[id]
	return source, ok
}

func (r *Registry) ToolSourceNames() []string {
	names := make([]string, 0, len(r.toolSources))
	for name := range r.toolSources {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func DefaultRegistry() (*Registry, error) {
	registry := NewRegistry()
	if err := registry.Register("memory", base.NewMemory(base.Config{})); err != nil {
		return nil, err
	}
	return registry, nil
}

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

func (r *Registry) Lookup(name string) (base.Adapter, bool) {
	implementation, ok := r.adapters[name]
	return implementation, ok
}

func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.adapters))
	for name := range r.adapters {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *Registry) adaptersLen() int { return len(r.adapters) }

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
	for _, id := range sortedKeys(file.ToolSources) {
		entry := file.ToolSources[id]

		if err := validateToolSource(id, entry.Kind, entry.Command, entry.Environment); err != nil {
			return nil, err
		}
		environment, err := resolveToolSourceEnvironment(entry.Environment, environ)
		if err != nil {
			return nil, fmt.Errorf("serve: tool source %q: %w", id, err)
		}
		source := protocol.ToolSourceAttachment{
			ID: id, Kind: entry.Kind, DisplayName: entry.DisplayName, Protocol: entry.Protocol,
			Endpoint: entry.Endpoint, Command: entry.Command, Args: entry.Args, Environment: environment,
		}
		if err := registry.RegisterToolSource(id, source); err != nil {
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

func resolveToolSourceEnvironment(entries []string, environ func(string) (string, bool)) ([]string, error) {
	for _, entry := range entries {
		name, _, literal := strings.Cut(entry, "=")
		if literal || name == "" {
			continue
		}
		if _, ok := environ(name); !ok {
			return nil, fmt.Errorf("environment variable %q is not set; export it or write %s=<value>", name, name)
		}
	}
	return resolveEnvironment(entries, environ)
}

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
