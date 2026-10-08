package sdk

import (
	"context"
	"errors"
)

func isAbort(err error) bool {
	var streamErr *StreamError
	if errors.As(err, &streamErr) && streamErr.Kind == KindAborted {
		return true
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func asStreamError(err error, target **StreamError) bool { return errors.As(err, target) }
