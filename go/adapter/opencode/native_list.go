package opencode

import (
	"context"
	"fmt"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/httpapi"
	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/native"
)

const nativeListPagesMax = 16

type sessionLister interface {
	Sessions(ctx context.Context, directory, search, cursor string, limit int) (httpapi.SessionPage, error)
}

func (a *Adapter) NativeList(ctx context.Context, request base.NativeListRequest) ([]base.NativeListing, error) {
	return a.sessions(ctx, request.Directory, request.Limit, "")
}

func (a *Adapter) NativeSearch(ctx context.Context, request base.NativeSearchRequest) ([]base.NativeListing, error) {
	return a.sessions(ctx, request.Directory, request.Limit, request.Term)
}

func (a *Adapter) sessions(ctx context.Context, directory string, limit int, term string) ([]base.NativeListing, error) {
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
	var infos []native.SessionInfo
	cursor := ""
	for pages := 0; pages < nativeListPagesMax && len(infos) < limit; pages++ {
		page, err := lister.Sessions(ctx, directory, term, cursor, limit-len(infos))
		if err != nil {
			return nil, fmt.Errorf("list OpenCode sessions: %w", err)
		}
		infos = append(infos, page.Data...)
		if len(page.Data) == 0 || page.Next == "" || page.Next == cursor {
			break
		}
		cursor = page.Next
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
