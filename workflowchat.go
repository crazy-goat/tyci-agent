package main

import (
	"github.com/crazy-goat/tyci-agent/internal/flow"
	"github.com/crazy-goat/tyci-agent/tools"
)

// workflowManager runs the chat workflow tools' runs (workflow_start and
// friends). Notices go to JobNotices, read at call time so tests can swap it.
var workflowManager = flow.NewManager(workflowNotify, tools.RunSubagentTask)

func workflowNotify(text string) { JobNotices.Notify(text) }
