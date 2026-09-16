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
	"github.com/lsm/open-agent-protocol/protocol"
)

// configFile is the oap serve registry document: named adapter entries, each
// mapping onto one in-repo adapter configuration.
type configFile struct {
	Adapters map[string]adapterEntry `json:"adapters"`
	// ToolSources are the process tool sources a wire caller may attach at
	// session open, by id only. The daemon fills the command, the arguments,
	// and the environment from here: "loopback, single-user" describes the
	// transport, not the origin of a request on it, and a page in the user's
	// browser can issue a cross-origin POST to 127.0.0.1 that the Host
	// allowlist admits. An executable the operator never configured is not
	// something the daemon should run under any boundary check.
	ToolSources map[string]toolSourceEntry `json:"tool_sources"`
}

// toolSourceEntry is one operator-configured tool source. Environment takes
// the adapter registry's allowlist form — a bare NAME forwards the daemon's own
// value, NAME=value passes literally — with one rule of its own: a bare name
// the daemon does not carry fails at hub start rather than being dropped.
//
// The adapter allowlist omits an unset name, and that is right for it: it is a
// broad "forward these if the daemon has them" list, written once for a
// harness. A tool source's list is not that. It names the credentials one
// executable needs, the daemon itself launches that executable with them, and
// the same reasoning that makes the daemon refuse a wire-supplied literal
// makes silently dropping one the wrong answer: the MCP server starts without
// its token and fails as though the server were broken, when the fault is one
// unexported name in the operator's own config. Failing at start says which id
// and which name, once, before anything depends on it. An operator who wants a
// name to be optional writes the literal form with an empty value.
type toolSourceEntry struct {
	Kind        string   `json:"kind"`
	DisplayName string   `json:"display_name"`
	Protocol    string   `json:"protocol"`
	Endpoint    string   `json:"endpoint"`
	Command     string   `json:"command"`
	Args        []string `json:"args"`
	Environment []string `json:"environment"`
}

// adapterEntry carries the fields the registry can express for the in-repo
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
// It also holds the operator-configured tool sources a wire caller may attach
// at session open by id.
type Registry struct {
	adapters    map[string]base.Adapter
	toolSources map[string]protocol.ToolSourceAttachment
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]base.Adapter), toolSources: make(map[string]protocol.ToolSourceAttachment)}
}

// RegisterToolSource adds one operator-configured tool source. Programmatic
// entries use the same surface as config-file entries; an embedding host that
// registers none accepts no wire-supplied process attachment at all.
//
// Because it is the same surface, it enforces the same rules. Both documented
// registration paths end here, so the entry is judged here rather than once per
// path: a rule stated at one entry point is a rule the other can be reached
// around, and the config loader's own check would have let an embedding host
// register exactly what it refuses in a file.
func (r *Registry) RegisterToolSource(id string, source protocol.ToolSourceAttachment) error {
	if id == "" {
		return errors.New("serve: tool source id is required")
	}
	if err := validateToolSource(id, source.Kind, source.Command); err != nil {
		return err
	}
	if _, exists := r.toolSources[id]; exists {
		return fmt.Errorf("serve: tool source %q is already registered", id)
	}
	source.ID = id
	r.toolSources[id] = source
	return nil
}

// validateToolSource judges one configured entry, whether it arrived from the
// registry document or from an embedding host's own call.
//
// A process source is the one kind the daemon supplies an executable for, and
// the command is the whole of what it supplies: without one there is nothing to
// spawn, so the entry can never resolve. It used to be stored anyway, and the
// error surfaced one open later as a generic open_failed from the adapter that
// could not start it — a 502 naming the session, not the entry that is wrong.
//
// Registering is not opening, so this does fail a host that registers an entry
// it never opens; that is the intended reach, and it is the narrower claim than
// it looks. Such an entry is unusable by construction: the only thing a
// registered tool source is for is being resolved at open, and every open that
// named this one already failed. Refusing it at registration moves the report
// to the call that can still be corrected, and says which id and which field.
//
// The rule is stated forwards only. A non-process entry carrying no command is
// complete, because nothing spawns it, and which kinds an operator may
// configure stays the adapter's disclosed transports to answer at admission,
// not this registry's.
func validateToolSource(id, kind, command string) error {
	if kind == "" {
		return fmt.Errorf("serve: tool source %q: kind is required", id)
	}
	if kind == protocol.ToolSourceProcess && command == "" {
		return fmt.Errorf("serve: tool source %q: a process source needs a command", id)
	}
	return nil
}

// ToolSource returns the operator-configured attachment registered under id.
func (r *Registry) ToolSource(id string) (protocol.ToolSourceAttachment, bool) {
	source, ok := r.toolSources[id]
	return source, ok
}

// ToolSourceNames returns the configured tool source ids in stable order.
func (r *Registry) ToolSourceNames() []string {
	names := make([]string, 0, len(r.toolSources))
	for name := range r.toolSources {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
// directories, executables, provider settings) surface at hub start rather
// than at first use. Environ resolves bare allowlist names against the
// embedding process's own environment; pass os.LookupEnv outside tests.
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
		// The entry's own shape is judged before anything is resolved for it, so
		// the more fundamental defect is still reported first. This is
		// RegisterToolSource's own check, called earlier rather than restated:
		// that call below runs it again on the assembled value, and one function
		// answers both registration paths.
		if err := validateToolSource(id, entry.Kind, entry.Command); err != nil {
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

// resolveEnvironment turns the config allowlist into the verbatim child
// environment: a bare NAME forwards the value the embedding process itself
// carries and is omitted entirely when unset, while NAME=value passes through
// literally. The result is never nil, so adapters that treat a nil environment
// as "inherit ambient" stay on an explicit allowlist and no ambient credential
// can reach a child process unless its variable was listed here.
// resolveToolSourceEnvironment resolves a tool source's allowlist under the
// stricter rule its doc comment states: a bare NAME the daemon does not carry
// is an error naming it, not an omission. Everything else is the adapter rule,
// so the two lists differ in exactly one place and only where they should.
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
