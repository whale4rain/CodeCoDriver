package lease

import (
	"context"
	"errors"
	"time"
)

var ErrStaleLease = errors.New("stale lease")

type Lease struct {
	TaskID   string
	WorkerID string
	Token    int64
}

type Leaser interface {
	TryClaim(context.Context, string, time.Duration) (Lease, bool, error)
	Renew(context.Context, Lease, time.Duration) error
	Release(context.Context, Lease) error
}
