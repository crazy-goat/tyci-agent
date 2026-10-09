package display

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// WorkflowParam is one positional param of a workflow, as the "/" popup shows it.
type WorkflowParam struct {
	Name     string
	Required bool
}

// WorkflowEntry describes one workflow for the "/" popup. Source is "project"
// or "home". Err is the reason the workflow cannot be started; an entry with
// Err is listed but never started by "/<name>".
type WorkflowEntry struct {
	Name   string
	Params []WorkflowParam
	Source string
	Err    string
}

// WorkflowStarter is what the TUI needs from the workflow runner. internal/flow
// implements it in package main, so this package does not import flow.
type WorkflowStarter interface {
	// List returns the workflows of the current repository, with the ones that
	// do not load.
	List() []WorkflowEntry
	// Start starts a run and returns at once. A missing required param returns a
	// WorkflowParamError and starts nothing.
	Start(name string, params []string) (run string, warnings []string, err error)
}

// WorkflowParamError is returned by WorkflowStarter.Start for a required param
// without a value.
type WorkflowParamError struct{ Name, Description string }

func (e WorkflowParamError) Error() string {
	return fmt.Sprintf("missing required param %s: %s", e.Name, e.Description)
}

// builtinCommands are the slash commands the TUI owns. A workflow cannot take
// one of these names: "/<name>" always means the builtin, and the workflow is
// still reachable with "/workflow <name>".
var builtinCommands = []struct{ name, desc string }{
	{"btw", "side question"},
	{"compact", "summarize the conversation"},
	{"exit", "quit"},
	{"msg", "send a message to a background job"},
	{"new", "start a new conversation"},
	{"resume", "resume a session"},
	{"workflow", "start a workflow by name"},
}

// IsReservedWorkflowName reports whether name is a builtin slash command.
func IsReservedWorkflowName(name string) bool {
	for _, c := range builtinCommands {
		if c.name == name {
			return true
		}
	}
	return false
}

// workflowLine splits a line that starts a workflow: "/workflow <name> params"
// or "/<name> params" for a workflow in list that can start. ok is false for
// every other line. A bare "/workflow" returns ok with an empty name.
func workflowLine(line string, list []WorkflowEntry) (name string, params []string, ok bool) {
	if !strings.HasPrefix(line, "/") {
		return "", nil, false
	}
	fields := strings.Fields(line[1:])
	if len(fields) == 0 {
		return "", nil, false
	}
	head := strings.ToLower(fields[0])
	if head == "workflow" {
		if len(fields) == 1 {
			return "", nil, true
		}
		return fields[1], fields[2:], true
	}
	if IsReservedWorkflowName(head) {
		return "", nil, false
	}
	for _, e := range list {
		if e.Name == head && e.Err == "" {
			return head, fields[1:], true
		}
	}
	return "", nil, false
}

// IsWorkflowLine reports whether line starts a workflow in the TUI. It returns
// false when s is nil.
func IsWorkflowLine(line string, s WorkflowStarter) bool {
	if s == nil {
		return false
	}
	_, _, ok := workflowLine(strings.TrimSpace(line), s.List())
	return ok
}

// workflowStartedMsg is the result of a start that runs in a tea.Cmd, so the
// event loop does not wait for the worktree to be prepared.
type workflowStartedMsg struct {
	notice    string // chat line for a started run
	modelText string // text for the chat model when a required param is missing
	err       error
}

// startWorkflow starts a workflow through s and turns the result into chat text.
// A missing required param does not fail: the text asks the model to ask the
// user, so the model calls workflow_start with the full param list.
func startWorkflow(s WorkflowStarter, name string, params []string) workflowStartedMsg {
	if name == "" {
		return workflowStartedMsg{err: errors.New("usage: /workflow <name> [params]")}
	}
	_, warnings, err := s.Start(name, params)
	var pe WorkflowParamError
	if errors.As(err, &pe) {
		call := strings.TrimSpace("/" + name + " " + strings.Join(params, " "))
		text := fmt.Sprintf("user wants %s, missing: %s", call, pe.Name)
		if pe.Description != "" {
			text += " (" + pe.Description + ")"
		}
		return workflowStartedMsg{modelText: text + ". Ask the user for it, then call workflow_start."}
	}
	if err != nil {
		return workflowStartedMsg{err: err}
	}
	notice := strings.TrimSpace("started /" + name + " " + strings.Join(params, " "))
	for _, w := range warnings {
		notice += "\nwarning: " + w
	}
	return workflowStartedMsg{notice: notice}
}

// startWorkflowFromInput takes a workflow line out of the input and starts it in
// a tea.Cmd. It is handled, and the turn is not touched, whether the agent is
// busy or not. Any other line returns handled false.
func (m *TuiModel) startWorkflowFromInput() (bool, tea.Cmd) {
	if m.workflows == nil {
		return false, nil
	}
	line := strings.TrimSpace(m.input.Value())
	name, params, ok := workflowLine(line, m.workflows.List())
	if !ok {
		return false, nil
	}
	m.input.Reset()
	m.input.SetHeight(1)
	m.closeFileComplete()
	m.closeSlashComplete()
	s := m.workflows
	return true, func() tea.Msg { return startWorkflow(s, name, params) }
}

// handleWorkflowStarted shows the result of a start. A missing param is sent to
// the model like a typed prompt, and the text the person is typing stays put.
func (m TuiModel) handleWorkflowStarted(msg workflowStartedMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.err != nil:
		cmd := m.handleBlockMsg(tuiMsgBlock{kind: "error", content: msg.err.Error()})
		return m, cmd
	case msg.modelText != "":
		typed := m.input.Value()
		m.input.SetValue(msg.modelText)
		next := m.submit().(TuiModel)
		next.input.SetValue(typed)
		return next, next.armStatusTick()
	default:
		cmd := m.handleBlockMsg(tuiMsgBlock{kind: "block", content: msg.notice})
		return m, cmd
	}
}
