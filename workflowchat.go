package main

import (
	"github.com/crazy-goat/tyci-agent/internal/flow"
	"github.com/crazy-goat/tyci-agent/tools"
)

// workflowManager runs the chat workflow tools' runs (workflow_start and
// friends). Notices go to the bus, read at call time so tests can swap appBus.
var workflowManager = flow.NewManager(workflowNotify, tools.RunSubagentTask)

func workflowNotify(text string) { publishNotice(appBus, "", text, false) }
