package serve

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/acp"
	"github.com/lsm/open-agent-protocol/go/protocol"
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

func TestLoadRegistryRefusesCaseVariantMembers(t *testing.T) {
	for name, document := range map[string]string{
		"top-level":   `{"Adapters": {}}`,
		"adapter":     `{"adapters": {"memory": {"Type": "memory"}}}`,
		"tool source": `{"tool_sources": {"fs": {"Kind": "native"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := writeConfig(t, document)
			if _, err := LoadRegistry(path, os.LookupEnv); err == nil {
				t.Fatal("a member whose case differs from the schema was accepted")
			}
		})
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
			"claude": {"type": "claude", "executable": "/bin/claude", "args": ["--flag"], "environment": ["HOME", "LITERAL=directory=v"], "working_directory": "/tmp", "model": "sonnet-5", "allowed_tools": ["Read", "Grep"]},
			"codex": {"type": "codex", "executable": "/bin/codex", "working_directory": "/tmp", "approval_policy": "never", "sandbox": "danger-full-access"},
			"hermes": {"type": "hermes", "executable": "/bin/python", "working_directory": "/tmp", "model": "glm-5.3"},
			"pi": {"type": "pi", "executable": "/bin/pi", "working_directory": "/tmp"},
			"acp": {"type": "acp", "executable": "/bin/cagent", "working_directory": "/tmp"},
			"deepseek": {"type": "deepseek", "executable": "/bin/dsh", "working_directory": "/tmp", "provider": "deepseek", "model": "deepseek-chat", "max_tokens": 4096},
			"opencode": {"type": "opencode", "endpoint": "http://127.0.0.1:4096", "agent": "code"}
		}
	}`)
	registry, err := LoadRegistry(path, staticEnviron(map[string]string{"HOME": "/home/tester"}))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"acp", "claude", "codex", "deepseek", "hermes", "opencode", "pi"}
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
		{"claude without a tool posture", `"claude": {"type": "claude", "executable": "/bin/claude", "working_directory": "/tmp"}`, []string{"claude", "allowed_tools", "unrestricted_tools"}},
		{"claude naming an empty tool", `"claude": {"type": "claude", "executable": "/bin/claude", "working_directory": "/tmp", "allowed_tools": [""]}`, []string{"claude", "allowed_tools", "empty tool"}},
		{"claude allowing nothing", `"claude": {"type": "claude", "executable": "/bin/claude", "working_directory": "/tmp", "allowed_tools": []}`, []string{"claude", "allowed_tools", "names no tool"}},
		{"claude stating both postures", `"claude": {"type": "claude", "executable": "/bin/claude", "working_directory": "/tmp", "allowed_tools": ["Read"], "unrestricted_tools": true}`, []string{"claude", "not both"}},
		{"acp relative directory", `"acp": {"type": "acp", "executable": "/bin/x", "working_directory": "relative"}`, []string{"acp", "absolute working directory"}},
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

func TestLoadRegistryToolSources(t *testing.T) {
	path := writeConfig(t, `{
		"adapters": {"memory": {"type": "memory"}},
		"tool_sources": {
			"filesystem": {
				"kind": "process", "protocol": "mcp", "display_name": "Filesystem",
				"endpoint": "stdio:filesystem-tools",
				"command": "/usr/local/bin/mcp-filesystem", "args": ["--root", "/workspace"],
				"environment": ["MCP_TOKEN", "MCP_MODE=readonly"]
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

func TestLoadRegistryToolSourceNeedsEveryNameItLists(t *testing.T) {
	config := `{
		"adapters": {"memory": {"type": "memory"}},
		"tool_sources": {
			"filesystem": {"kind": "process", "command": "/usr/local/bin/mcp-filesystem", "environment": [%s]}
		}
	}`
	path := writeConfig(t, fmt.Sprintf(config, `"MCP_TOKEN", "NEVER_EXPORTED"`))
	_, err := LoadRegistry(path, staticEnviron(map[string]string{"MCP_TOKEN": "operator-secret"}))
	if err == nil {
		t.Fatal("a tool source naming an unexported variable started the hub")
	}
	for _, want := range []string{"filesystem", "NEVER_EXPORTED"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("load error %q does not name %q", err, want)
		}
	}

	path = writeConfig(t, fmt.Sprintf(config, `"NEVER_EXPORTED="`))
	registry, err := LoadRegistry(path, staticEnviron(nil))
	if err != nil {
		t.Fatalf("an explicit empty value was refused: %v", err)
	}
	source, ok := registry.ToolSource("filesystem")
	if !ok {
		t.Fatal("configured tool source missing")
	}
	if strings.Join(source.Environment, ",") != "NEVER_EXPORTED=" {
		t.Fatalf("resolved environment: %v", source.Environment)
	}

	path = writeConfig(t, `{"adapters": {"claude": {"type": "claude", "executable": "/bin/claude", "working_directory": "/tmp", "environment": ["NEVER_EXPORTED"], "unrestricted_tools": true}}}`)
	if _, err := LoadRegistry(path, staticEnviron(nil)); err != nil {
		t.Fatalf("an adapter naming an unexported variable failed to load: %v", err)
	}
}

func TestLoadRegistryToolSourceNeedsKind(t *testing.T) {
	path := writeConfig(t, `{"adapters": {"memory": {"type": "memory"}}, "tool_sources": {"bare": {"command": "/bin/true"}}}`)
	if _, err := LoadRegistry(path, os.LookupEnv); err == nil || !strings.Contains(err.Error(), "kind is required") {
		t.Fatalf("load error = %v", err)
	}
}

func TestLoadRegistryProcessToolSourceNeedsCommand(t *testing.T) {
	path := writeConfig(t, `{"adapters": {"memory": {"type": "memory"}}, "tool_sources": {"filesystem": {"kind": "process", "endpoint": "stdio:filesystem"}}}`)
	if _, err := LoadRegistry(path, os.LookupEnv); err == nil || !strings.Contains(err.Error(), "a process source needs a command") {
		t.Fatalf("load error = %v", err)
	}

	path = writeConfig(t, `{"adapters": {"memory": {"type": "memory"}}, "tool_sources": {"bogus-tools": {"kind": "bogus"}}}`)
	if _, err := LoadRegistry(path, os.LookupEnv); err == nil || !strings.Contains(err.Error(), "is not a tool source kind") {
		t.Fatalf("load error = %v", err)
	}

	path = writeConfig(t, `{"adapters": {"memory": {"type": "memory"}}, "tool_sources": {"remote-tools": {"kind": "remote", "endpoint": "https://tools.example"}}}`)
	registry, err := LoadRegistry(path, os.LookupEnv)
	if err != nil {
		t.Fatalf("a commandless remote source was refused: %v", err)
	}
	if source, ok := registry.ToolSource("remote-tools"); !ok || source.Kind != protocol.ToolSourceRemote {
		t.Fatalf("configured tool source = %+v (%v)", source, ok)
	}
}

func TestRegisterToolSourceJudgesTheEntryTheLoaderWouldHaveJudged(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		source protocol.ToolSourceAttachment
		want   string
	}{
		{"no kind", protocol.ToolSourceAttachment{Command: "/usr/local/bin/mcp-filesystem"}, "kind is required"},

		{"a kind outside the protocol", protocol.ToolSourceAttachment{Kind: "bogus"}, `kind "bogus" is not a tool source kind`},

		{"one variable named twice", protocol.ToolSourceAttachment{
			Kind: protocol.ToolSourceRemote, Environment: []string{"TOKEN", "TOKEN=literal"},
		}, `environment names "TOKEN" twice`},
		{"a process source with no command", protocol.ToolSourceAttachment{Kind: protocol.ToolSourceProcess}, "a process source needs a command"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			registry := NewRegistry()
			err := registry.RegisterToolSource("filesystem", testCase.source)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("register error = %v, want one reporting %q", err, testCase.want)
			}
			if _, ok := registry.ToolSource("filesystem"); ok {
				t.Fatal("a refused entry was stored anyway")
			}
		})
	}

	registry := NewRegistry()
	for id, source := range map[string]protocol.ToolSourceAttachment{
		"filesystem":   {Kind: protocol.ToolSourceProcess, Command: "/usr/local/bin/mcp-filesystem"},
		"remote-tools": {Kind: protocol.ToolSourceRemote, Endpoint: "https://tools.example"},
	} {
		if err := registry.RegisterToolSource(id, source); err != nil {
			t.Fatalf("a complete %q entry was refused: %v", id, err)
		}
	}
	if names := registry.ToolSourceNames(); strings.Join(names, ",") != "filesystem,remote-tools" {
		t.Fatalf("registered sources = %v", names)
	}
}

func TestEveryAdapterRefusesUnadvertisedToolSources(t *testing.T) {
	path := writeConfig(t, `{
		"adapters": {
			"claude": {"type": "claude", "executable": "/bin/claude", "working_directory": "/tmp", "unrestricted_tools": true},
			"codex": {"type": "codex", "executable": "/bin/codex", "working_directory": "/tmp"},
			"hermes": {"type": "hermes", "executable": "/bin/python", "working_directory": "/tmp", "model": "glm-5.3"},
			"pi": {"type": "pi", "executable": "/bin/pi", "working_directory": "/tmp"},
			"deepseek": {"type": "deepseek", "executable": "/bin/dsh", "working_directory": "/tmp", "provider": "deepseek", "model": "deepseek-chat"},
			"opencode": {"type": "opencode", "endpoint": "http://127.0.0.1:4096"}
		}
	}`)
	registry, err := LoadRegistry(path, os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	request := base.OpenRequest{
		SessionID:   "attached",
		Participant: protocol.Participant{ID: DefaultParticipant},
		ToolSources: []protocol.ToolSourceAttachment{{ID: "files", Kind: protocol.ToolSourceProcess, Command: "/bin/true"}},
	}
	for _, name := range registry.Names() {
		t.Run(name, func(t *testing.T) {
			implementation, ok := registry.Lookup(name)
			if !ok {
				t.Fatalf("adapter %q missing from the registry it was built from", name)
			}

			descriptor, err := implementation.Probe(context.Background())
			if err != nil {
				t.Fatalf("probe: %v", err)
			}
			if support, advertised := descriptor.Capabilities.EffectiveSupport(protocol.FeatureToolSourcesAttach); advertised && support.Level != protocol.SupportUnavailable {
				t.Fatalf("%s advertises %s at %q; this test covers the adapters that do not", name, protocol.FeatureToolSourcesAttach, support.Level)
			}
			session, err := implementation.Open(context.Background(), request)
			if err == nil {
				_ = session.Close(context.Background())
				t.Fatal("the open was admitted, discarding the attachment it was given")
			}
			var refusal *base.UnsupportedControlError
			if !errors.As(err, &refusal) {
				t.Fatalf("refusal is %v, want *adapter.UnsupportedControlError", err)
			}
			if refusal.Feature != protocol.FeatureToolSourcesAttach || refusal.Reason != base.ControlUnadvertised {
				t.Fatalf("refusal names %s/%s", refusal.Feature, refusal.Reason)
			}
		})
	}
}

func TestAdvertisingAdaptersAdmitToolSources(t *testing.T) {
	attaching := base.OpenRequest{ToolSources: []protocol.ToolSourceAttachment{{ID: "files", Kind: protocol.ToolSourceProcess, Command: "/bin/true"}}}

	acpAdapter, err := acp.New(acp.Config{Executable: "/bin/acp", WorkingDirectory: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	for name, implementation := range map[string]base.Adapter{"memory": base.NewMemory(base.Config{}), "acp": acpAdapter} {
		t.Run(name, func(t *testing.T) {
			descriptor, err := implementation.Probe(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			support, advertised := descriptor.Capabilities.EffectiveSupport(protocol.FeatureToolSourcesAttach)
			if !advertised {
				t.Fatalf("%s no longer advertises %s; this test covers the adapters that do", name, protocol.FeatureToolSourcesAttach)
			}
			if err := base.RefuseUnadvertisedToolSources(attaching, support); err != nil {
				t.Fatalf("an advertised attachment was refused: %v", err)
			}
		})
	}

	if err := base.RefuseUnadvertisedToolSources(base.OpenRequest{}); err != nil {
		t.Fatalf("an open attaching nothing was refused: %v", err)
	}
}

func TestSharedGateRefusesADisclosureAnOpenCannotElect(t *testing.T) {
	attaching := base.OpenRequest{ToolSources: []protocol.ToolSourceAttachment{{ID: "files", Kind: protocol.ToolSourceProcess, Command: "/bin/true"}}}
	for _, testCase := range []struct {
		name      string
		disclosed []protocol.FeatureSupport
	}{
		{"no disclosure at all", nil},
		{"an unavailable level", []protocol.FeatureSupport{{Level: protocol.SupportUnavailable, Modes: []string{protocol.ModeSessionOpen}}}},
		{"a level with no mode", []protocol.FeatureSupport{{Level: protocol.SupportNative}}},
		{"a mode an open cannot elect", []protocol.FeatureSupport{{Level: protocol.SupportNative, Modes: []string{protocol.ModeRemote}}}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var refusal *base.UnsupportedControlError
			if err := base.RefuseUnadvertisedToolSources(attaching, testCase.disclosed...); !errors.As(err, &refusal) {
				t.Fatalf("refusal is %v, want *adapter.UnsupportedControlError", err)
			}
			if refusal.Feature != protocol.FeatureToolSourcesAttach || refusal.Reason != base.ControlUnadvertised {
				t.Fatalf("refusal names %s/%s", refusal.Feature, refusal.Reason)
			}
		})
	}
}
