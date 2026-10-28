package forge

import (
	"context"
	"time"
)

// CIRun is the newest completed CI run of a branch.
type CIRun struct {
	// ID is the forge id of the run.
	ID int64
	// SHA is the head commit of the run.
	SHA string
	// Failed is true when the conclusion is not success.
	Failed bool
	// CreatedAt is the time the forge created the run.
	CreatedAt time.Time
}

// CIReader reads the CI state of a branch. It is optional: a forge without
// it gives no ci_failure trigger in the daemon.
type CIReader interface {
	// LatestCI returns the newest completed run of workflow on branch.
	// Cancelled, skipped and neutral runs are ignored, like
	// internal/flow/checks/ci_failure.sh does. ok is false when the branch has
	// no completed run.
	LatestCI(ctx context.Context, workflow, branch string) (run CIRun, ok bool, err error)
}

// Commenter posts a comment on an issue. It is optional: the daemon only
// logs when the forge cannot comment.
type Commenter interface {
	// Comment posts body as a new comment on issue.
	Comment(ctx context.Context, issue int, body string) error
}
