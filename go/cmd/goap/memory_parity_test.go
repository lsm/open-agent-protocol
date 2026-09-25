package main

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

var memoryParityScenarios = map[string][]string{
	"open elections and refusals": {
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "protocol.initialize.request", "id": "i1", "payload": {"protocol_versions": ["0.1"], "profiles": ["open-agent-protocol.agent-control-core"], "participant": {"id": "user"}}}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "capabilities.request", "id": "c1", "payload": {}}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "session.open.request", "id": "o1", "payload": {"session_id": "demo", "tool_sources": [{"id": "att1", "kind": "process", "display_name": "Attached", "protocol": "mcp", "endpoint": "stdio:x", "command": "/bin/x", "environment": ["A=1"]}], "tools": [{"name": "lookup", "description": "Look it up.", "input_schema": {"type": "object"}, "execution_owner": "user", "source": "att1"}]}}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "session.open.request", "id": "o2", "payload": {"session_id": "bad", "tools": [{"name": "Bad-Name", "execution_owner": "user"}]}}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "session.open.request", "id": "o3", "payload": {"session_id": "bad2", "tool_sources": [{"id": "reference-mcp", "kind": "process"}]}}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "models.request", "id": "m1", "payload": {"session_id": "demo"}, "session_id": "demo", "capability_revision": "reference-memory-v11"}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "action.tools.list.request", "id": "t1", "payload": {"session_id": "demo"}, "session_id": "demo", "capability_revision": "reference-memory-v11"}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "session.state.request", "id": "st0", "payload": {"session_id": "demo"}, "session_id": "demo"}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "session.message.submit.request", "id": "s1", "payload": {"session_id": "demo", "delivery": "auto", "messages": [{"role": "user", "content": [{"type": "text", "text": "run"}]}]}, "session_id": "demo"}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "session.state.request", "id": "st1", "payload": {"session_id": "demo"}, "session_id": "demo"}`,
	},
	"provided tool, queue, gates and cancel": {
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "protocol.initialize.request", "id": "i1", "payload": {"protocol_versions": ["0.1"], "profiles": ["open-agent-protocol.agent-control-core"], "participant": {"id": "user"}}}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "session.open.request", "id": "o1", "payload": {"session_id": "demo", "tool_sources": [{"id": "att1", "kind": "local", "display_name": "Attached"}], "tools": [{"name": "lookup", "description": "Look it up.", "input_schema": {"type": "object"}, "execution_owner": "user", "source": "att1"}]}}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "session.open.request", "id": "o2", "payload": {"session_id": "bad", "tools": [{"name": "Bad-Name", "execution_owner": "user"}]}}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "session.open.request", "id": "o3", "payload": {"session_id": "bad2", "tool_sources": [{"id": "reference-mcp", "kind": "local"}]}}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "session.open.request", "id": "o4", "payload": {"session_id": "bad3", "tools": [{"name": "x", "execution_owner": "someone"}]}}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "models.request", "id": "m1", "payload": {"session_id": "demo"}, "session_id": "demo", "capability_revision": "reference-memory-v11"}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "action.tools.list.request", "id": "t1", "payload": {"session_id": "demo"}, "session_id": "demo", "capability_revision": "reference-memory-v11"}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "action.tools.list.request", "id": "t2", "payload": {}, "capability_revision": "reference-memory-v11"}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "session.state.request", "id": "st0", "payload": {"session_id": "demo"}, "session_id": "demo"}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "session.open.request", "id": "o5", "payload": {"session_id": "demo", "tool_sources": [{"id": "att9", "kind": "local", "command": "/bin/sh"}]}}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "session.message.submit.request", "id": "tc1", "payload": {"session_id": "demo", "delivery": "auto", "tool_choice": null, "messages": [{"role": "user", "content": "run"}]}, "session_id": "demo", "capability_revision": "reference-memory-v11"}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "session.message.submit.request", "id": "tc2", "payload": {"session_id": "demo", "delivery": "auto", "tool_choice": {"allowed": [], "disallowed": []}, "messages": [{"role": "user", "content": "run"}]}, "session_id": "demo", "capability_revision": "reference-memory-v11"}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "session.message.submit.request", "id": "tc3", "payload": {"session_id": "demo", "delivery": "auto", "tool_choice": {"allowed": null}, "messages": [{"role": "user", "content": "run"}]}, "session_id": "demo", "capability_revision": "reference-memory-v11"}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "session.message.submit.request", "id": "tc4", "payload": {"session_id": "demo", "delivery": "auto", "tool_choice": {}, "messages": [{"role": "user", "content": "run"}]}, "session_id": "demo", "capability_revision": "reference-memory-v11"}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "session.message.submit.request", "id": "tc5", "payload": {"session_id": "demo", "delivery": "auto", "tool_choice": {"other": []}, "messages": [{"role": "user", "content": "run"}]}, "session_id": "demo", "capability_revision": "reference-memory-v11"}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "session.message.submit.request", "id": "tc7", "payload": {"session_id": "demo", "delivery": "auto", "tool_choice": 5, "messages": [{"role": "user", "content": "run"}]}, "session_id": "demo", "capability_revision": "reference-memory-v11"}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "session.message.submit.request", "id": "s1", "payload": {"session_id": "demo", "delivery": "auto", "messages": [{"role": "user", "content": [{"type": "text", "text": "run"}]}]}, "session_id": "demo"}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "session.message.submit.request", "id": "s2", "payload": {"session_id": "demo", "delivery": "auto", "messages": [{"role": "user", "content": [{"type": "text", "text": "again"}]}]}, "session_id": "demo"}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "session.state.request", "id": "st1", "payload": {"session_id": "demo"}, "session_id": "demo"}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "action.call.resolve.request", "id": "rc0", "payload": {"session_id": "demo", "run_id": "run-1", "tool_call_id": "tool-call-4", "requested_by": "reference.memory", "responded_by": "intruder", "interaction_id": "call-5", "started": {}}, "session_id": "demo", "run_id": "run-1", "capability_revision": "reference-memory-v11"}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "action.call.resolve.request", "id": "rc1", "payload": {"session_id": "demo", "run_id": "run-1", "tool_call_id": "tool-call-4", "requested_by": "reference.memory", "responded_by": "user", "interaction_id": "call-5", "started": {}}, "session_id": "demo", "run_id": "run-1", "capability_revision": "reference-memory-v11"}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "action.call.resolve.request", "id": "rc2", "payload": {"session_id": "demo", "run_id": "run-1", "tool_call_id": "tool-call-4", "requested_by": "reference.memory", "responded_by": "user", "interaction_id": "call-5", "started": {}}, "session_id": "demo", "run_id": "run-1", "capability_revision": "reference-memory-v11"}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "session.state.request", "id": "st2", "payload": {"session_id": "demo"}, "session_id": "demo"}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "action.call.resolve.request", "id": "rc3", "payload": {"session_id": "demo", "run_id": "run-1", "tool_call_id": "tool-call-4", "requested_by": "reference.memory", "responded_by": "user", "interaction_id": "call-5", "result": "found"}, "session_id": "demo", "run_id": "run-1", "capability_revision": "reference-memory-v11"}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "action.call.resolve.request", "id": "rc4", "payload": {"session_id": "demo", "run_id": "run-1", "tool_call_id": "tool-call-4", "requested_by": "reference.memory", "responded_by": "user", "interaction_id": "call-5", "result": {"hits": 2}}, "session_id": "demo", "run_id": "run-1", "capability_revision": "reference-memory-v11"}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "user.input.resolve.request", "id": "ui1", "payload": {"interaction_id": "input-3", "session_id": "demo", "run_id": "run-1", "requested_by": "reference.memory", "responded_by": "user", "answers": [{"question_id": "choice", "selected_option_ids": ["yes"]}]}, "session_id": "demo", "run_id": "run-1"}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "session.state.request", "id": "st3", "payload": {"session_id": "demo"}, "session_id": "demo"}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "run.cancel.request", "id": "c9", "payload": {"session_id": "demo", "run_id": "run-6"}, "session_id": "demo", "run_id": "run-6"}`,
		`{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "session.state.request", "id": "st4", "payload": {"session_id": "demo"}, "session_id": "demo"}`,
	},
}

func TestMemoryBackendMatchesOapx(t *testing.T) {
	oapx := os.Getenv("OAP_OAPX_BIN")
	if oapx == "" {
		t.Skip("set OAP_OAPX_BIN to an oapx binary to compare its memory backend with goap's")
	}
	if !filepath.IsAbs(oapx) {
		t.Fatalf("OAP_OAPX_BIN must be absolute, got %q", oapx)
	}
	goap := oapBinary(t)
	for name, lines := range memoryParityScenarios {
		t.Run(name, func(t *testing.T) {
			want := normalizedExchange(t, exec.Command(goap, "serve", "agent"), lines)
			got := normalizedExchange(t, exec.Command(oapx, "serve", "agent", "--backend", "memory"), lines)
			if strings.Join(want, "\n") != strings.Join(got, "\n") {
				t.Fatalf("oapx diverges from goap\n--- goap\n%s\n--- oapx\n%s", strings.Join(want, "\n"), strings.Join(got, "\n"))
			}
		})
	}
}

func normalizedExchange(t *testing.T, cmd *exec.Cmd, lines []string) []string {
	t.Helper()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	received := make(chan string, 256)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1<<20), 1<<20)
		for scanner.Scan() {
			received <- scanner.Text()
		}
		close(received)
	}()
	for _, line := range lines {
		if _, err := io.WriteString(stdin, line+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	var out []string
	quiet := time.NewTimer(time.Second)
	for waiting := true; waiting; {
		select {
		case line, ok := <-received:
			if !ok {
				waiting = false
				break
			}
			out = append(out, normalizedLine(t, line))
			quiet.Reset(500 * time.Millisecond)
		case <-quiet.C:
			waiting = false
		}
	}
	stdin.Close()
	_ = cmd.Wait()
	sort.Strings(out)
	return out
}
