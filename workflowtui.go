package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/crazy-goat/tyci-agent/display"
	"github.com/crazy-goat/tyci-agent/internal/flow"
)

// workflowStarter starts the workflows typed in the TUI as "/<name>" (see
// display/tui_workflow.go). It uses the same manager as the workflow_* tools.
var workflowStarter display.WorkflowStarter = flowWorkflows{M: workflowManager}

// flowWorkflows adapts a flow.Manager to display.WorkflowStarter. It calls
// Manager.Start directly, because ChatTools.Start turns a missing param into a
// plain error and prints a model notice.
type flowWorkflows struct{ M *flow.Manager }

// List implements display.WorkflowStarter. It lists the workflows of the
// current repository, including the ones that do not load. A reserved name
// gets an error, so it is never started by "/<name>".
func (w flowWorkflows) List() []display.WorkflowEntry {
	info, err := w.M.Info()
	if err != nil {
		return nil
	}
	var out []display.WorkflowEntry
	for _, l := range flow.Listings(info) {
		e := display.WorkflowEntry{Name: l.Name, Source: l.Source}
		for _, p := range l.Params {
			e.Params = append(e.Params, display.WorkflowParam{Name: p.Name, Required: p.Required})
		}
		switch {
		case l.Err != nil:
			e.Err = l.Err.Error()
		case display.IsReservedWorkflowName(l.Name):
			e.Err = fmt.Sprintf("name is reserved: /%s is a builtin command; start it with /workflow %s", l.Name, l.Name)
		}
		out = append(out, e)
	}
	return out
}

// Start implements display.WorkflowStarter.
func (w flowWorkflows) Start(name string, params []string) (string, []string, error) {
	run, warnings, err := w.M.Start(context.Background(), flow.StartRequest{Workflow: name, Params: params})
	var mp flow.MissingParamError
	if errors.As(err, &mp) {
		return "", nil, display.WorkflowParamError{Name: mp.Name, Description: mp.Description}
	}
	return run, warnings, err
}

// reservedWorkflowErrors returns the start-up errors of the workflows whose
// names are builtin commands. Other workflow errors are shown in the "/" popup.
func reservedWorkflowErrors(list []display.WorkflowEntry) []error {
	var errs []error
	for _, e := range list {
		if display.IsReservedWorkflowName(e.Name) && e.Err != "" {
			errs = append(errs, errors.New("workflow "+e.Name+": "+e.Err))
		}
	}
	return errs
}
