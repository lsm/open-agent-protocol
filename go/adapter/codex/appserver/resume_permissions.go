package appserver

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/lsm/open-agent-protocol/go/adapter/codex/appserver/internal/native"
)

var sandboxPolicyTypes = map[string]string{
	"read-only":          "readOnly",
	"workspace-write":    "workspaceWrite",
	"danger-full-access": "dangerFullAccess",
}

func confirmsHostPermissions(sent native.ThreadResumeParams, answered native.ThreadResumeResponse) error {
	if sent.Sandbox != "" {
		wanted, mapped := sandboxPolicyTypes[sent.Sandbox]
		if !mapped {
			return fmt.Errorf("the configured sandbox %s has no thread/resume policy the adapter can confirm", sent.Sandbox)
		}
		var policy struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(answered.Sandbox, &policy); err != nil || policy.Type != wanted {
			return fmt.Errorf("Codex resumed the thread under sandbox %s, not the configured %s", string(answered.Sandbox), sent.Sandbox)
		}
	}
	if sent.ApprovalPolicy != "" {
		var policy string
		if err := json.Unmarshal(answered.ApprovalPolicy, &policy); err != nil || policy != sent.ApprovalPolicy {
			return fmt.Errorf("Codex resumed the thread under approval policy %s, not the configured %s", string(answered.ApprovalPolicy), sent.ApprovalPolicy)
		}
	}
	if sent.Cwd != "" && filepath.Clean(answered.Cwd) != filepath.Clean(sent.Cwd) {
		return fmt.Errorf("Codex resumed the thread in %q, not the configured %q", answered.Cwd, sent.Cwd)
	}
	return nil
}
