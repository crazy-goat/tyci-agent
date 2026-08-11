package jobs

import (
	"context"
	"time"
)

type Waiter interface {
	Wait(ctx context.Context, id string, timeout time.Duration) (*Job, bool)
}

type Lister interface {
	List() []Job
}

var _ Waiter = (*Registry)(nil)
var _ Lister = (*Registry)(nil)
