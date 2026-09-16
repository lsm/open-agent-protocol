package serve

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/acp"
	"github.com/lsm/open-agent-protocol/protocol"
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
// at load — a bare NAME takes the daemon's own value and NAME=value passes
// through literally. Resolving here rather than at open means a misconfigured
// source fails at hub start.
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

// TestLoadRegistryToolSourceNeedsEveryNameItLists is the one place a tool
// source's allowlist is stricter than an adapter's. An adapter's list is a
// broad "forward these if the daemon has them", written once for a harness; a
// tool source's names the credentials one executable needs, and the daemon
// launches that executable itself. Dropping an unexported name would start the
// MCP server without its token, and it would fail as though the server were
// broken when the fault is one name in the operator's own config. The failure
// names the id and the variable, before anything depends on either.
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

	// The literal form is the escape hatch for a name that is meant to be
	// optional: it says what the value is rather than hoping for one.
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

	// An adapter's own allowlist keeps the omitting rule, so the divergence is
	// exactly where it was argued for and nowhere else.
	path = writeConfig(t, `{"adapters": {"claude": {"type": "claude", "executable": "/bin/claude", "working_directory": "/tmp", "environment": ["NEVER_EXPORTED"]}}}`)
	if _, err := LoadRegistry(path, staticEnviron(nil)); err != nil {
		t.Fatalf("an adapter naming an unexported variable failed to load: %v", err)
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

// TestLoadRegistryProcessToolSourceNeedsCommand refuses the entry the daemon
// could never resolve, on the config path; the programmatic path is held to
// the same check by the test above, because the two share one. A process source is the one kind the daemon supplies an
// executable for, and the command is the whole of what it supplies: without
// one there is nothing to spawn. The entry used to load, and the first open
// naming it failed inside the adapter as an invalid resolution, which the route
// reports as a generic open_failed — a 502 about a session, for a defect in one
// line of the operator's config.
//
// Nothing that worked stops working: an entry in this shape was already
// unusable and every open naming it already failed. Only where the failure is
// reported changes, which is the argument the environment rule above makes too.
func TestLoadRegistryProcessToolSourceNeedsCommand(t *testing.T) {
	path := writeConfig(t, `{"adapters": {"memory": {"type": "memory"}}, "tool_sources": {"filesystem": {"kind": "process", "endpoint": "stdio:filesystem"}}}`)
	if _, err := LoadRegistry(path, os.LookupEnv); err == nil || !strings.Contains(err.Error(), "a process source needs a command") {
		t.Fatalf("load error = %v", err)
	}

	// A kind outside the protocol is refused on this path too, because both
	// paths share one check.
	path = writeConfig(t, `{"adapters": {"memory": {"type": "memory"}}, "tool_sources": {"bogus-tools": {"kind": "bogus"}}}`)
	if _, err := LoadRegistry(path, os.LookupEnv); err == nil || !strings.Contains(err.Error(), "is not a tool source kind") {
		t.Fatalf("load error = %v", err)
	}

	// The command rule is stated forwards only. A non-process entry carrying no
	// command is a complete entry, because nothing spawns it; which of the
	// protocol's kinds an operator may configure is the adapter's disclosed
	// transports to answer at admission, not this loader's.
	path = writeConfig(t, `{"adapters": {"memory": {"type": "memory"}}, "tool_sources": {"remote-tools": {"kind": "remote", "endpoint": "https://tools.example"}}}`)
	registry, err := LoadRegistry(path, os.LookupEnv)
	if err != nil {
		t.Fatalf("a commandless remote source was refused: %v", err)
	}
	if source, ok := registry.ToolSource("remote-tools"); !ok || source.Kind != protocol.ToolSourceRemote {
		t.Fatalf("configured tool source = %+v (%v)", source, ok)
	}
}

// TestRegisterToolSourceJudgesTheEntryTheLoaderWouldHaveJudged holds the
// programmatic path to the rules the config path states, because they are one
// surface and not two. An embedding host calls RegisterToolSource with exactly
// the value LoadRegistry builds, so a rule enforced only while parsing a file
// is a rule the other caller reaches around: it could register precisely the
// entry a registry document is refused for, and the defect would surface one
// open later as a generic open_failed.
//
// Registering is not opening, so this does fail a host that registers an entry
// it never opens. That reach is intended and narrower than it looks: the only
// thing a registered tool source is for is being resolved at open, so such an
// entry is unusable by construction and every open that named it already
// failed. Only where the failure is reported moves.
func TestRegisterToolSourceJudgesTheEntryTheLoaderWouldHaveJudged(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		source protocol.ToolSourceAttachment
		want   string
	}{
		{"no kind", protocol.ToolSourceAttachment{Command: "/usr/local/bin/mcp-filesystem"}, "kind is required"},
		// A kind outside the protocol is dead config in both directions: the
		// schema refuses it on the wire, so no client can name it, and a request
		// naming any valid kind is refused for contradicting the configured one.
		// That is well-formedness, the same class as the empty id — not a
		// judgement about which of the protocol's transports are allowed, which
		// stays the adapter's to make.
		{"a kind outside the protocol", protocol.ToolSourceAttachment{Kind: "bogus"}, `kind "bogus" is not a tool source kind`},
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

	// The complete entries both paths accept, including the non-process kind
	// that needs no command: the rule is stated forwards only.
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

// TestEveryAdapterRefusesUnadvertisedToolSources is the fail-closed contract
// held across the whole registry rather than per adapter. OpenRequest.ToolSources
// is a field an adapter written before the tool-sources unit never reads, so
// without an explicit gate such an adapter returns a successful session having
// silently dropped the sources the caller asked for — and a caller cannot tell
// that session from one that attached them.
//
// Every adapter that does not advertise action.tool_sources.attach must
// therefore refuse the open with the typed error naming the key, before any
// process starts: the refusals below are all returned ahead of the adapter's
// client factory, so a registry of executables that do not exist still answers.
func TestEveryAdapterRefusesUnadvertisedToolSources(t *testing.T) {
	path := writeConfig(t, `{
		"adapters": {
			"claude": {"type": "claude", "executable": "/bin/claude", "working_directory": "/tmp"},
			"codex": {"type": "codex", "executable": "/bin/codex", "working_directory": "/tmp"},
			"hermes": {"type": "hermes", "executable": "/bin/python", "working_directory": "/tmp", "model": "glm-5.3"},
			"pi": {"type": "pi", "executable": "/bin/pi", "working_directory": "/tmp"},
			"makai": {"type": "makai", "executable": "/bin/makai", "working_directory": "/tmp", "agent_config": {}},
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
			// Every key this adapter advertises above `unavailable` must be
			// absent, or the refusal below would be wrong rather than owed.
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

// TestAdvertisingAdaptersAdmitToolSources is the other direction: an endpoint
// that does advertise the key must not be refused by the shared gate, or the
// gate would make the advertisement unusable. Each adapter is fed its own
// published disclosure rather than a hand-written one, so an adapter whose
// descriptor stopped admitting attachment at open would fail here rather than
// in a corpus somewhere.
func TestAdvertisingAdaptersAdmitToolSources(t *testing.T) {
	attaching := base.OpenRequest{ToolSources: []protocol.ToolSourceAttachment{{ID: "files", Kind: protocol.ToolSourceProcess, Command: "/bin/true"}}}
	// The executable never runs: Probe is static and the gate answers before
	// any child is started.
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
	// An open carrying no sources is never refused, whatever the endpoint
	// advertises: an open that elects nothing owes nothing.
	if err := base.RefuseUnadvertisedToolSources(base.OpenRequest{}); err != nil {
		t.Fatalf("an open attaching nothing was refused: %v", err)
	}
}

// TestSharedGateRefusesADisclosureAnOpenCannotElect closes the asymmetry that
// would otherwise sit between the two paths: the daemon's open route and the
// validator both hold an attach capability to disclosing a session-open mode,
// and an adapter that admitted one without it would give an in-process
// embedder a silently-attaching open where a wire caller is refused. The gate
// is the one place both paths agree, so it judges the disclosure, not the key
// name.
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
