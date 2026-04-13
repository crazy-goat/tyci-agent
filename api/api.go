package api

import (
	"encoding/json"
	"fmt"
	"os"
)

type UsageInfo struct {
	InputTokens  int
	OutputTokens int
	Cost         float64
}

type ToolCall struct {
	Index    int
	Name     string
	Argument string
}

type StreamHandler interface {
	Chunk(text string)
	Thinking(text string)
	EndThinking()
	LogToolCallStart(name string)
	ToolCallArg(text string)
	EndToolCall()
	Summary(usage UsageInfo)
	End()
	Error(err error)
}

type DebugHandler struct {
	Inner           StreamHandler
	Debug           bool
	HideThinking    bool
	HideTools       bool
	ToolCalls       []ToolCall
	thinkingActive  bool
	thinkingStarted bool
}

func (d *DebugHandler) Chunk(text string) {
	d.Inner.Chunk(text)
	if d.Debug {
		fmt.Fprintf(os.Stderr, "[CHUNK] %s\n", text)
	}
}

func (d *DebugHandler) Thinking(text string) {
	d.Inner.Thinking(text)
	if d.HideThinking {
		return
	}

	// Model sends reasoning as delta chunks, just print them
	if !d.thinkingStarted {
		fmt.Fprintf(os.Stderr, "💭 %s", text)
		d.thinkingStarted = true
	} else {
		fmt.Fprintf(os.Stderr, "%s", text)
	}
	d.thinkingActive = true
}

func (d *DebugHandler) EndThinking() {
	if !d.HideThinking && d.thinkingActive {
		fmt.Fprintf(os.Stderr, "\n\n")
		d.thinkingActive = false
		d.thinkingStarted = false
	}
	d.Inner.EndThinking()
}

func (d *DebugHandler) LogToolCallStart(name string) {
	d.Inner.LogToolCallStart(name)
}

func (d *DebugHandler) ToolCallArg(text string) {
	d.Inner.ToolCallArg(text)
}

func (d *DebugHandler) EndToolCall() {
	d.Inner.EndToolCall()
}

func (d *DebugHandler) Summary(usage UsageInfo) {
	d.Inner.Summary(usage)
}

func (d *DebugHandler) End() {
	d.Inner.End()
}

func (d *DebugHandler) Error(err error) {
	d.Inner.Error(err)
}

func (d *DebugHandler) LogRequest(method, url string, body any) {
	if !d.Debug {
		return
	}
	jsonBody, _ := json.Marshal(body)
	fmt.Fprintf(os.Stderr, "[DEBUG REQ] %s %s\n%s\n", method, url, string(jsonBody))
}

func (d *DebugHandler) LogResponse(data string) {
	if !d.Debug {
		return
	}
	fmt.Fprintf(os.Stderr, "[DEBUG RESP] %s\n", data)
}

func (d *DebugHandler) AccumulateToolCall(idx int, name, argument string) {
	for len(d.ToolCalls) <= idx {
		d.ToolCalls = append(d.ToolCalls, ToolCall{Index: len(d.ToolCalls)})
	}
	if name != "" {
		d.ToolCalls[idx].Name = name
	}
	d.ToolCalls[idx].Argument += argument
	if d.Debug {
		fmt.Fprintf(os.Stderr, "[TOOL_CALL] idx=%d name=%q arg_accumulated=%q\n", idx, d.ToolCalls[idx].Name, d.ToolCalls[idx].Argument)
	}
}

func (d *DebugHandler) GetToolCalls() []ToolCall {
	return d.ToolCalls
}

func (d *DebugHandler) ResetToolCalls() {
	d.ToolCalls = nil
}
