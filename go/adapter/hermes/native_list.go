package hermes

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"strconv"

	base "github.com/lsm/open-agent-protocol/go/adapter"
)

const methodSessionList = "session.list"

type listingFactory interface {
	List(context.Context, int) (json.RawMessage, error)
}

type sessionListParams struct {
	Limit int `json:"limit"`
}

func (f processClientFactory) List(ctx context.Context, limit int) (json.RawMessage, error) {
	p, client, _, err := f.launch(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = p.Close(context.Background()) }()
	var answered json.RawMessage
	if err := client.Call(ctx, methodSessionList, sessionListParams{Limit: limit}, &answered); err != nil {
		return nil, err
	}
	return answered, nil
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
