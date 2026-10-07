package appserver

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/lsm/open-agent-protocol/go/adapter"
)

const (
	nativeTurnPage   = 100
	nativeTitleLimit = 120
)

func (implementation *Adapter) NativeLink(nativeID string) string {
	return "codex://threads/" + nativeID
}

func ask(ctx context.Context, client Client, method string, params any) (json.RawMessage, error) {
	var result json.RawMessage
	if err := client.Call(ctx, method, params, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (implementation *Adapter) NativeList(ctx context.Context, request adapter.NativeListRequest) ([]adapter.NativeListing, error) {
	directory := request.Directory
	if directory == "" {
		directory = implementation.config.WorkingDirectory
	}
	params := map[string]any{"limit": request.Limit}
	if directory != "" {
		params["cwd"] = directory
	}
	client, err := implementation.config.Factory.Start(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = client.Close() }()
	result, err := ask(ctx, client, "thread/list", params)
	if err != nil {
		return nil, err
	}
	return implementation.threadsOf(result), nil
}

func (implementation *Adapter) threadsOf(result json.RawMessage) []adapter.NativeListing {
	var body struct {
		Data []json.RawMessage `json:"data"`
	}
	if json.Unmarshal(result, &body) != nil {
		return nil
	}
	var listed []adapter.NativeListing
	for _, raw := range body.Data {
		var thread map[string]json.RawMessage
		if json.Unmarshal(raw, &thread) != nil {
			continue
		}
		id := memberText(thread, "id")
		if id == "" {
			continue
		}
		title := memberText(thread, "name")
		if title == "" {
			preview, _, _ := strings.Cut(memberText(thread, "preview"), "\n")
			title = preview[:titleCut(preview, nativeTitleLimit)]
		}
		var status map[string]json.RawMessage
		_ = json.Unmarshal(thread["status"], &status)
		listed = append(listed, adapter.NativeListing{
			NativeID:    id,
			Title:       title,
			Directory:   memberText(thread, "cwd"),
			UpdatedAtMS: memberSeconds(thread, "updatedAt"),
			Running:     memberText(status, "type") == "active",
			Link:        implementation.NativeLink(id),
		})
	}
	return listed
}

func (implementation *Adapter) NativeRead(ctx context.Context, request adapter.NativeReadRequest) ([]adapter.NativeTurn, error) {
	client, err := implementation.config.Factory.Start(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = client.Close() }()
	var found []adapter.NativeTurn
	cursor := ""
	pagesMax := request.MaxTurns/nativeTurnPage + 2
	for pages := 0; pages < pagesMax; pages++ {
		params := map[string]any{"threadId": request.NativeID, "itemsView": "full", "sortDirection": "asc", "limit": nativeTurnPage}
		if cursor != "" {
			params["cursor"] = cursor
		}
		page, err := ask(ctx, client, "thread/turns/list", params)
		if err != nil {
			return nil, err
		}
		found = append(found, turnsOf(page)...)
		if len(found) >= request.MaxTurns {
			break
		}
		var next struct {
			NextCursor json.RawMessage `json:"nextCursor"`
		}
		_ = json.Unmarshal(page, &next)
		cursor = ""
		_ = json.Unmarshal(next.NextCursor, &cursor)
		if cursor == "" {
			break
		}
	}
	return found, nil
}

func turnsOf(page json.RawMessage) []adapter.NativeTurn {
	var body struct {
		Data []json.RawMessage `json:"data"`
	}
	if json.Unmarshal(page, &body) != nil {
		return nil
	}
	var found []adapter.NativeTurn
	for _, raw := range body.Data {
		var turn map[string]json.RawMessage
		if json.Unmarshal(raw, &turn) != nil {
			continue
		}
		var items []json.RawMessage
		if json.Unmarshal(turn["items"], &items) != nil {
			continue
		}
		var said strings.Builder
		reply := ""
		for _, rawItem := range items {
			var item map[string]json.RawMessage
			if json.Unmarshal(rawItem, &item) != nil {
				continue
			}
			switch memberText(item, "type") {
			case "userMessage":
				var content []json.RawMessage
				if json.Unmarshal(item["content"], &content) != nil {
					continue
				}
				for _, rawInput := range content {
					var input map[string]json.RawMessage
					if json.Unmarshal(rawInput, &input) != nil || memberText(input, "type") != "text" {
						continue
					}
					if said.Len() > 0 {
						said.WriteByte('\n')
					}
					said.WriteString(memberText(input, "text"))
				}
			case "agentMessage":
				if text := memberText(item, "text"); text != "" {
					reply = text
				}
			}
		}
		if said.Len() > 0 {
			found = append(found, adapter.NativeTurn{Role: "user", Text: said.String(), AtMS: memberSeconds(turn, "startedAt")})
		}
		if reply != "" {
			found = append(found, adapter.NativeTurn{Role: "assistant", Text: reply, AtMS: memberSeconds(turn, "completedAt")})
		}
	}
	return found
}

func memberText(object map[string]json.RawMessage, name string) string {
	var text string
	_ = json.Unmarshal(object[name], &text)
	return text
}

func memberSeconds(object map[string]json.RawMessage, name string) int64 {
	var seconds int64
	if json.Unmarshal(object[name], &seconds) != nil {
		return 0
	}
	return seconds * 1000
}

func titleCut(text string, limit int) int {
	if len(text) <= limit {
		return len(text)
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return cut
}
