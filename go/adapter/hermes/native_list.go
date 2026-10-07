package hermes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"sync"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/hermes/internal/rpc"
)

const methodSessionList = "session.list"

type listingFactory interface {
	List(context.Context, int) (json.RawMessage, error)
}

type sessionListParams struct {
	Limit int `json:"limit"`
}

type listGateway struct {
	mu     sync.Mutex
	bridge ProcessBridge
}

func listOn(ctx context.Context, client Client, limit int) (json.RawMessage, error) {
	var answered json.RawMessage
	if err := client.Call(ctx, methodSessionList, sessionListParams{Limit: limit}, &answered); err != nil {
		return nil, err
	}
	return answered, nil
}

func (f processClientFactory) List(ctx context.Context, limit int) (json.RawMessage, error) {
	kept := f.lister
	if kept == nil || !kept.mu.TryLock() {
		p, client, _, err := f.launch(ctx)
		if err != nil {
			return nil, err
		}
		defer func() { _ = p.Close(context.Background()) }()
		return listOn(ctx, client, limit)
	}
	defer kept.mu.Unlock()
	reused := kept.bridge != nil
	if reused && gatewayEnded(kept.bridge) {
		_ = kept.bridge.Close(context.Background())
		kept.bridge, reused = nil, false
	}
	if kept.bridge == nil {
		bridge, err := f.keep(ctx)
		if err != nil {
			return nil, err
		}
		kept.bridge = bridge
	}
	answered, err := listOn(ctx, kept.bridge.ClientHandle(), limit)
	if err == nil || !gatewayBroken(kept.bridge, err) {
		return answered, err
	}
	_ = kept.bridge.Close(context.Background())
	kept.bridge = nil
	if !reused {
		return nil, err
	}
	bridge, err := f.keep(ctx)
	if err != nil {
		return nil, err
	}
	kept.bridge = bridge
	answered, err = listOn(ctx, bridge.ClientHandle(), limit)
	if err != nil && gatewayBroken(bridge, err) {
		_ = bridge.Close(context.Background())
		kept.bridge = nil
	}
	return answered, err
}

func (f processClientFactory) keep(ctx context.Context) (ProcessBridge, error) {
	bridge, err := f.processes.Start(context.WithoutCancel(ctx), f.config)
	if err != nil {
		return nil, err
	}
	client := bridge.ClientHandle()
	go func() {
		for {
			select {
			case message, open := <-client.Inbound():
				if !open {
					return
				}
				if message.Barrier != nil {
					close(message.Barrier)
				}
			case <-client.Done():
				return
			}
		}
	}()
	return bridge, nil
}

func gatewayEnded(bridge ProcessBridge) bool {
	select {
	case <-bridge.Done():
		return true
	default:
		return false
	}
}

func gatewayBroken(bridge ProcessBridge, err error) bool {
	var remote *rpc.RemoteError
	return !errors.As(err, &remote) || gatewayEnded(bridge)
}

func (a *Adapter) NativeList(ctx context.Context, request base.NativeListRequest) ([]base.NativeListing, error) {
	listing, ok := a.config.Factory.(listingFactory)
	if !ok {
		return nil, nil
	}
	answered, err := listing.List(ctx, request.Limit)
	if err != nil {
		return nil, err
	}
	var page struct {
		Sessions json.RawMessage `json:"sessions"`
	}
	var rows []json.RawMessage
	if json.Unmarshal(answered, &page) != nil || json.Unmarshal(page.Sessions, &rows) != nil {
		return nil, nil
	}
	var listed []base.NativeListing
	for _, raw := range rows {
		if len(listed) >= request.Limit {
			break
		}
		var row map[string]json.RawMessage
		_ = json.Unmarshal(raw, &row)
		id := rowText(row, "id")
		if id == "" {
			continue
		}
		title := rowText(row, "title")
		if title == "" {
			title = rowText(row, "preview")
		}
		listed = append(listed, base.NativeListing{NativeID: id, Title: title, UpdatedAtMS: startedMillis(row["started_at"])})
	}
	return listed, nil
}

func rowText(row map[string]json.RawMessage, name string) string {
	var text string
	_ = json.Unmarshal(row[name], &text)
	return text
}

func startedMillis(raw json.RawMessage) int64 {
	text := string(bytes.TrimSpace(raw))
	if text == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(text, 10, 64); err == nil {
		if seconds > math.MaxInt64/1000 {
			return math.MaxInt64
		}
		if seconds < math.MinInt64/1000 {
			return math.MinInt64
		}
		return seconds * 1000
	}
	seconds, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsInf(seconds, 0) || math.IsNaN(seconds) || math.Abs(seconds) >= 1e15 {
		return 0
	}
	return int64(seconds * 1000)
}
