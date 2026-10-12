package tools

import (
	"github.com/crazy-goat/tyci-agent/jobs"
)

// regJobWaiterForBashTest is a local, test-only mirror of btw.go's
// jobWaiterAdapter (package main, which this package must not import — see
// JobWaiter's own doc comment). It copies every JobStatus field, so it does
// not diverge in what it carries — but being a mirror it can never catch a
// regression in the real adapter. That is deliberate, not an oversight:
// this test's job is the bash -> registry -> renderProgressHistory pipeline,
// and the production adapters are covered separately by btw_test.go's
// TestJobWaiterAdapter_TranslatesProgressHistoryAndTruncation and its
// observer twin, which drive the real jobWaiterAdapter/jobObserverAdapter.
type regJobWaiterForBashTest struct{ reg *jobs.Registry }
