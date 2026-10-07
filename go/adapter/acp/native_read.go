package acp

import (
	"context"
	"encoding/json"
	"strings"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/rpc"
)

func loadOffered(capabilities rpc.AgentCapabilities) bool {
	var load bool
	return json.Unmarshal(capabilities["loadSession"], &load) == nil && load
}

func (a *Adapter) NativeRead(ctx context.Context, request base.NativeReadRequest) ([]base.NativeTurn, error) {
	client, initialized, err := a.config.Factory.Start(ctx)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	if !loadOffered(initialized.AgentCapabilities) {
		return nil, nil
	}
	directory := request.Directory
	if directory == "" {
		directory = a.config.WorkingDirectory
	}
	var replayed []json.RawMessage
	collect := func(message rpc.InboundMessage) {
		if message.Notification != nil && message.Notification.Method == native.MethodSessionUpdate {
			replayed = append(replayed, message.Notification.Params)
		}
	}
	var loaded json.RawMessage
	if err := callCollecting(ctx, client, native.MethodSessionLoad, native.SessionReopenParams{SessionID: request.NativeID, Cwd: directory, MCPServers: []native.MCPServer{}}, &loaded, collect); err != nil {
		return nil, err
	}
	return replayedTurns(replayed), nil
}

func replayedTurns(updates []json.RawMessage) []base.NativeTurn {
	var found []base.NativeTurn
	var asked *strings.Builder
	var reply strings.Builder
	replyID := ""
	flushAsked := func() {
		if asked == nil {
			return
		}
		if said := strings.Trim(asked.String(), " \t\r\n"); said != "" {
			found = append(found, base.NativeTurn{Role: "user", Text: said})
		}
		asked = nil
	}
	for _, raw := range updates {
		var params struct {
			Update struct {
				SessionUpdate string `json:"sessionUpdate"`
				MessageID     string `json:"messageId"`
				Content       struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"update"`
		}
		if json.Unmarshal(raw, &params) != nil {
			continue
		}
		update := params.Update
		switch update.SessionUpdate {
		case "user_message_chunk":
			if asked == nil {
				if reply.Len() > 0 {
					found = append(found, base.NativeTurn{Role: "assistant", Text: reply.String()})
				}
				reply.Reset()
				replyID = ""
				asked = &strings.Builder{}
			}
			asked.WriteString(update.Content.Text)
		case "agent_message_chunk":
			flushAsked()
			if update.MessageID != "" && update.MessageID != replyID {
				reply.Reset()
				replyID = update.MessageID
			}
			reply.WriteString(update.Content.Text)
		}
	}
	flushAsked()
	if reply.Len() > 0 {
		found = append(found, base.NativeTurn{Role: "assistant", Text: reply.String()})
	}
	return found
}
