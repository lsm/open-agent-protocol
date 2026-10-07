package opencode

import (
	"context"
	"fmt"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/native"
)

type sessionLister interface {
	Sessions(ctx context.Context, directory string, limit int) ([]native.SessionInfo, error)
}

func (a *Adapter) NativeList(ctx context.Context, request base.NativeListRequest) ([]base.NativeListing, error) {
	ctx, cancel := context.WithTimeout(ctx, a.config.RequestTimeout)
	defer cancel()
	client, err := a.config.Factory.Start(ctx)
	if err != nil {
		return nil, fmt.Errorf("list OpenCode sessions: %w", err)
	}
	defer func() { _ = client.Close() }()
	lister, ok := client.(sessionLister)
	if !ok {
		return nil, nil
	}
	infos, err := lister.Sessions(ctx, request.Directory, request.Limit)
	if err != nil {
		return nil, fmt.Errorf("list OpenCode sessions: %w", err)
	}
	running, err := client.Active(ctx)
	if err != nil {
		return nil, fmt.Errorf("list running OpenCode sessions: %w", err)
	}
	listed := make([]base.NativeListing, 0, len(infos))
	for _, info := range infos {
		listed = append(listed, base.NativeListing{NativeID: string(info.ID), Title: info.Title, Directory: info.Directory(), UpdatedAtMS: info.Time.Updated, Running: running[info.ID]})
	}
	return listed, nil
}
