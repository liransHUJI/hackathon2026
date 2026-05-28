package cancel

import (
	"context"
	"errors"
)

var ErrCancelled = errors.New("job cancellation requested")

type Checker interface {
	IsCancelled(context.Context, string) (bool, error)
}

func Check(ctx context.Context, checker Checker, jobID string) error {
	if checker == nil || jobID == "" {
		return nil
	}
	cancelled, err := checker.IsCancelled(ctx, jobID)
	if err != nil {
		return err
	}
	if cancelled {
		return ErrCancelled
	}
	return nil
}
