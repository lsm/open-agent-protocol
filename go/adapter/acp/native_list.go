package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/rpc"
)

const nativeListPagesMax = 16

type sessionListParams struct {
	Cwd    string `json:"cwd"`
	Cursor string `json:"cursor,omitempty"`
}

type sessionListPage struct {
	Sessions   json.RawMessage `json:"sessions"`
	NextCursor json.RawMessage `json:"nextCursor"`
}

func textField(entry map[string]json.RawMessage, name string) string {
	var text string
	_ = json.Unmarshal(entry[name], &text)
	return text
}

func listOffered(capabilities rpc.AgentCapabilities) bool {
	var session struct {
		List json.RawMessage `json:"list"`
	}
	if json.Unmarshal(capabilities["sessionCapabilities"], &session) != nil {
		return false
	}
	listed := bytes.TrimSpace(session.List)
	return len(listed) > 0 && !bytes.Equal(listed, []byte("null"))
}

func (a *Adapter) NativeList(ctx context.Context, request base.NativeListRequest) ([]base.NativeListing, error) {
	client, initialized, err := a.config.Factory.Start(ctx)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	if !listOffered(initialized.AgentCapabilities) {
		return nil, nil
	}
	directory := request.Directory
	if directory == "" {
		directory = a.config.WorkingDirectory
	}
	var listed []base.NativeListing
	cursor := ""
	for pages := 0; len(listed) < request.Limit && pages < nativeListPagesMax; pages++ {
		var page sessionListPage
		if err := client.Call(ctx, "session/list", sessionListParams{Cwd: directory, Cursor: cursor}, &page); err != nil {
			return nil, err
		}
		var sessions []json.RawMessage
		if json.Unmarshal(page.Sessions, &sessions) != nil {
			break
		}
		for _, raw := range sessions {
			if len(listed) >= request.Limit {
				break
			}
			var entry map[string]json.RawMessage
			_ = json.Unmarshal(raw, &entry)
			id := textField(entry, "sessionId")
			if id == "" {
				continue
			}
			listed = append(listed, base.NativeListing{NativeID: id, Title: textField(entry, "title"), Directory: textField(entry, "cwd"), UpdatedAtMS: isoMillis(textField(entry, "updatedAt"))})
		}
		var next string
		_ = json.Unmarshal(page.NextCursor, &next)
		if next == "" || next == cursor {
			break
		}
		cursor = next
	}
	return listed, nil
}

func isoMillis(text string) int64 {
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return 0
	}
	return parsed.UnixMilli()
}
