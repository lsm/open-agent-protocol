package acp_test

import (
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/go/adapter/acp"
)

func TestAHostOutsideThePackageConfiguresMCPServersThatTheAdapterChecks(t *testing.T) {
	servers := []acp.MCPServer{
		{Name: "files", Command: "files-mcp", Env: []acp.EnvVariable{{Name: "ROOT", Value: "/repo"}}},
		{Name: "files", Command: "other"},
	}
	_, err := acp.New(acp.Config{Executable: "agent", WorkingDirectory: "/repo", MCPServers: servers})
	if err == nil || !strings.Contains(err.Error(), "configured twice") {
		t.Fatalf("New = %v, want the duplicate MCP server refused", err)
	}
	if _, err := acp.New(acp.Config{Executable: "agent", WorkingDirectory: "/repo", MCPServers: servers[:1]}); err != nil {
		t.Fatal(err)
	}
}
