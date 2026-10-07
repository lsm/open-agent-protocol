package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/httpapi"
	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/native"
)

const (
	nativeReadPage     = 200
	nativeReadPagesMax = 16
)

type messageReader interface {
	Messages(ctx context.Context, session native.SessionID, cursor string, limit int) (httpapi.MessagePage, error)
}

type storedMessage struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Time struct {
		Created int64 `json:"created"`
	} `json:"time"`
}

func (a *Adapter) NativeRead(ctx context.Context, request base.NativeReadRequest) ([]base.NativeTurn, error) {
	ctx, cancel := context.WithTimeout(ctx, a.config.RequestTimeout)
	defer cancel()
	client, err := a.config.Factory.Start(ctx)
	if err != nil {
		return nil, fmt.Errorf("read OpenCode session: %w", err)
	}
	defer func() { _ = client.Close() }()
	reader, ok := client.(messageReader)
	if !ok {
		return nil, nil
	}
	var messages []json.RawMessage
	cursor := ""
	for pages := 0; pages < nativeReadPagesMax; pages++ {
		page, err := reader.Messages(ctx, native.SessionID(request.NativeID), cursor, nativeReadPage)
		if err != nil {
			return nil, fmt.Errorf("read OpenCode session: %w", err)
		}
		messages = append(messages, page.Data...)
		if page.Cursor.Next == "" || page.Cursor.Next == cursor {
			break
		}
		cursor = page.Cursor.Next
	}
	return turnsOf(messages), nil
}

func turnsOf(messages []json.RawMessage) []base.NativeTurn {
	var turns []base.NativeTurn
	var reply string
	var replyAt int64
	for _, raw := range messages {
		var message storedMessage
		if json.Unmarshal(raw, &message) != nil {
			continue
		}
		switch message.Type {
		case "user":
			said := strings.Trim(message.Text, " \t\r\n")
			if said == "" {
				continue
			}
			if reply != "" {
				turns = append(turns, base.NativeTurn{Role: "assistant", Text: reply, AtMS: replyAt})
			}
			reply = ""
			turns = append(turns, base.NativeTurn{Role: "user", Text: said, AtMS: message.Time.Created})
		case "assistant":
			var piece strings.Builder
			for _, part := range message.Content {
				if part.Type == "text" {
					piece.WriteString(part.Text)
				}
			}
			if piece.Len() > 0 {
				reply, replyAt = piece.String(), message.Time.Created
			}
		}
	}
	if reply != "" {
		turns = append(turns, base.NativeTurn{Role: "assistant", Text: reply, AtMS: replyAt})
	}
	return turns
}
