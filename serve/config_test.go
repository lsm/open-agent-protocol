package serve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	base "github.com/lsm/open-agent-protocol/adapter"
)

func writeConfig(t *testing.T, document string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "oap.json")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func staticEnviron(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func mustMemory() base.Adapter {
	return base.NewMemory(base.Config{})
}

func TestLoadRegistryMemory(t *testing.T) {
	path := writeConfig(t, `{"adapters": {"memory": {"type": "memory", "journal_capacity": 8}}}`)
	registry, err := LoadRegistry(path, os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	if names := registry.Names(); len(names) != 1 || names[0] != "memory" {
		t.Fatalf("names: %v", names)
	}
	if _, ok := registry.Lookup("memory"); !ok {
		t.Fatal("memory adapter missing")
	}
	if _, ok := registry.Lookup("other"); ok {
		t.Fatal("unknown adapter resolved")
	}
}

func TestLoadRegistryDefaultsTypeToEntryName(t *testing.T) {
	path := writeConfig(t, `{"adapters": {"memory": {"journal_capacity": 3}}}`)
	registry, err := LoadRegistry(path, os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Lookup("memory"); !ok {
		t.Fatal("entry did not default to the memory type")
	}
}

func TestLoadRegistrySortedNames(t *testing.T) {
	path := writeConfig(t, `{"adapters": {"zeta": {"type": "memory"}, "alpha": {"type": "memory"}}}`)
	registry, err := LoadRegistry(path, os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(registry.Names(), ","); got != "alpha,zeta" {
		t.Fatalf("names not sorted: %q", got)
	}
}

func TestDefaultRegistry(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if names := registry.Names(); len(names) != 1 || names[0] != "memory" {
		t.Fatalf("default registry: %v", names)
	}
}

func TestLoadRegistryProcessAdapters(t *testing.T) {
	path := writeConfig(t, `{
		"adapters": {
			"claude": {"type": "claude", "executable": "/bin/claude", "args": ["--flag"], "environment": ["HOME", "LITERAL=directory=v"], "working_directory": "/tmp", "model": "sonnet-5"},
			"codex": {"type": "codex", "executable": "/bin/codex", "working_directory": "/tmp", "approval_policy": "never", "sandbox": "danger-full-access"},
			"hermes": {"type": "hermes", "executable": "/bin/python", "working_directory": "/tmp", "model": "glm-5.3"},
			"pi": {"type": "pi", "executable": "/bin/pi", "working_directory": "/tmp"},
			"acp": {"type": "acp", "executable": "/bin/cagent", "working_directory": "/tmp"},
			"makai": {"type": "makai", "executable": "/bin/makai", "working_directory": "/tmp", "agent_config": {}, "system_prompt": "be brief"},
			"deepseek": {"type": "deepseek", "executable": "/bin/dsh", "working_directory": "/tmp", "provider": "deepseek", "model": "deepseek-chat", "max_tokens": 4096},
			"opencode": {"type": "opencode", "endpoint": "http://127.0.0.1:4096", "agent": "code"}
		}
	}`)
	registry, err := LoadRegistry(path, staticEnviron(map[string]string{"HOME": "/home/tester"}))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"acp", "claude", "codex", "deepseek", "hermes", "makai", "opencode", "pi"}
	if got := strings.Join(registry.Names(), ","); got != strings.Join(want, ",") {
		t.Fatalf("names: %q", got)
	}
}

func TestLoadRegistryConstructorErrors(t *testing.T) {
	cases := []struct {
		name   string
		entry  string
		wanted []string
	}{
		{"claude without executable", `"claude": {"type": "claude"}`, []string{"claude", "executable"}},
		{"acp relative directory", `"acp": {"type": "acp", "executable": "/bin/x", "working_directory": "relative"}`, []string{"acp", "absolute working directory"}},
		{"makai without agent config", `"makai": {"type": "makai", "executable": "/bin/x", "working_directory": "/tmp"}`, []string{"makai", "agent config"}},
		{"deepseek without provider", `"deepseek": {"type": "deepseek", "executable": "/bin/x", "working_directory": "/tmp", "model": "m"}`, []string{"deepseek", "provider"}},
		{"opencode without endpoint", `"opencode": {"type": "opencode"}`, []string{"opencode", "endpoint"}},
		{"unknown type", `"ghost": {"type": "ghost"}`, []string{"ghost", "unknown type"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			path := writeConfig(t, `{"adapters": {`+testCase.entry+`}}`)
			_, err := LoadRegistry(path, os.LookupEnv)
			if err == nil {
				t.Fatal("expected a load error")
			}
			for _, want := range testCase.wanted {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q lacks %q", err, want)
				}
			}
		})
	}
}

func TestLoadRegistryDocumentErrors(t *testing.T) {
	cases := []struct {
		name     string
		document string
		want     string
	}{
		{"malformed json", `{"adapters":`, "parse config"},
		{"unknown field", `{"adapters": {"memory": {"journal_capcity": 2}}}`, "unknown field"},
		{"trailing data", `{"adapters": {}} {}`, "trailing data"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			path := writeConfig(t, testCase.document)
			_, err := LoadRegistry(path, os.LookupEnv)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error %v lacks %q", err, testCase.want)
			}
		})
	}
	_, err := LoadRegistry(filepath.Join(t.TempDir(), "missing.json"), os.LookupEnv)
	if err == nil || !strings.Contains(err.Error(), "read config") {
		t.Fatalf("missing file error: %v", err)
	}
}

func TestResolveEnvironment(t *testing.T) {
	environ := staticEnviron(map[string]string{"SET": "value", "EMPTY": ""})
	cases := []struct {
		name    string
		entries []string
		want    []string
	}{
		{"allowlist only", []string{"SET"}, []string{"SET=value"}},
		{"unset name omitted", []string{"UNSET"}, []string{}},
		{"empty value forwarded", []string{"EMPTY"}, []string{"EMPTY="}},
		{"literal pair", []string{"A=b"}, []string{"A=b"}},
		{"literal with equals", []string{"A=b=c"}, []string{"A=b=c"}},
		{"order preserved", []string{"B=2", "SET", "A=1"}, []string{"B=2", "SET=value", "A=1"}},
		{"empty allowlist", []string{}, []string{}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := resolveEnvironment(testCase.entries, environ)
			if err != nil {
				t.Fatal(err)
			}
			if got == nil {
				t.Fatal("resolved environment must never be nil")
			}
			if strings.Join(got, "\x00") != strings.Join(testCase.want, "\x00") {
				t.Fatalf("resolved %q, want %q", got, testCase.want)
			}
		})
	}
	for _, entry := range []string{"", "=value"} {
		if _, err := resolveEnvironment([]string{entry}, environ); err == nil {
			t.Fatalf("entry %q must be rejected", entry)
		}
	}
}

func TestRegistryRegister(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register("memory", mustMemory()); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("memory", mustMemory()); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("duplicate registration error: %v", err)
	}
	if err := registry.Register("", mustMemory()); err == nil {
		t.Fatal("empty name must be rejected")
	}
}

// TestLoadRegistryToolSources pins the operator-configured attachment surface:
// the entry a client may name by id, with the environment allowlist resolved
// at load — a bare NAME takes the daemon's own value, a name the operator
// never exported is dropped, and NAME=value passes through literally. Resolving
// here rather than at open means a misconfigured source fails at hub start.
func TestLoadRegistryToolSources(t *testing.T) {
	path := writeConfig(t, `{
		"adapters": {"memory": {"type": "memory"}},
		"tool_sources": {
			"filesystem": {
				"kind": "process", "protocol": "mcp", "display_name": "Filesystem",
				"endpoint": "stdio:filesystem-tools",
				"command": "/usr/local/bin/mcp-filesystem", "args": ["--root", "/workspace"],
				"environment": ["MCP_TOKEN", "NEVER_EXPORTED", "MCP_MODE=readonly"]
			}
		}
	}`)
	registry, err := LoadRegistry(path, staticEnviron(map[string]string{"MCP_TOKEN": "operator-secret"}))
	if err != nil {
		t.Fatal(err)
	}
	if names := registry.ToolSourceNames(); len(names) != 1 || names[0] != "filesystem" {
		t.Fatalf("tool source names: %v", names)
	}
	source, ok := registry.ToolSource("filesystem")
	if !ok {
		t.Fatal("configured tool source missing")
	}
	if source.ID != "filesystem" || source.Command != "/usr/local/bin/mcp-filesystem" || len(source.Args) != 2 {
		t.Fatalf("tool source: %+v", source)
	}
	if strings.Join(source.Environment, ",") != "MCP_TOKEN=operator-secret,MCP_MODE=readonly" {
		t.Fatalf("resolved environment: %v", source.Environment)
	}
	if _, ok := registry.ToolSource("never-configured"); ok {
		t.Fatal("an unconfigured tool source resolved")
	}
}

// TestLoadRegistryToolSourceNeedsKind refuses an entry that says nothing about
// what it is: kind is what decides whether an attachment is even eligible for
// the transports an adapter discloses.
func TestLoadRegistryToolSourceNeedsKind(t *testing.T) {
	path := writeConfig(t, `{"adapters": {"memory": {"type": "memory"}}, "tool_sources": {"bare": {"command": "/bin/true"}}}`)
	if _, err := LoadRegistry(path, os.LookupEnv); err == nil || !strings.Contains(err.Error(), "kind is required") {
		t.Fatalf("load error = %v", err)
	}
}
