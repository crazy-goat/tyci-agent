package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

type RetryableError struct {
	Code       int
	RetryAfter string
	Message    string
}

func (e *RetryableError) Error() string { return e.Message }

func IsRetryable(err error) bool {
	var re *RetryableError
	if errors.As(err, &re) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	return false
}

type RetryConfig struct {
	MaxRetries  int
	BaseBackoff int
	MaxBackoff  int
}

func (c RetryConfig) WithDefaults() RetryConfig {
	if c.MaxRetries == 0 {
		c.MaxRetries = 5
	}
	if c.BaseBackoff == 0 {
		c.BaseBackoff = 4
	}
	if c.MaxBackoff == 0 {
		c.MaxBackoff = 128
	}
	return c
}

func CalcBackoff(attempt int, err error, config RetryConfig) time.Duration {
	config = config.WithDefaults()
	var re *RetryableError
	if errors.As(err, &re) && re.RetryAfter != "" {
		if d, parseErr := strconv.Atoi(strings.TrimSpace(re.RetryAfter)); parseErr == nil {
			dur := time.Duration(d) * time.Second
			maxDur := time.Duration(config.MaxBackoff) * time.Second
			if dur > maxDur {
				dur = maxDur
			}
			return dur
		}
	}
	backoff := config.BaseBackoff * (1 << attempt)
	maxDur := time.Duration(config.MaxBackoff) * time.Second
	dur := time.Duration(backoff) * time.Second
	if dur > maxDur {
		dur = maxDur
	}
	return dur
}

func SleepWithCountdown(backoff time.Duration, attempt, maxRetries int, err error) {
	remaining := int(backoff.Seconds())
	prefix := fmt.Sprintf("⟳ retry %d/%d", attempt+1, maxRetries)
	reason := err.Error()
	fmt.Fprintf(os.Stderr, "%s — %s\n", prefix, reason)
	for remaining > 0 {
		time.Sleep(1 * time.Second)
		remaining--
		if remaining > 0 {
			fmt.Fprintf(os.Stderr, "\r%s — %s — next in %ds... ", prefix, reason, remaining)
		}
	}
	fmt.Fprintf(os.Stderr, "\r%s — %s — retrying...                    \n", prefix, reason)
}

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

var (
	bgThinking   = "\033[48;2;248;253;248m"
	bgReset      = "\033[0m"
	clearLine    = "\033[K"
	StderrOutput bool
)

func TerminalIsDark() bool {
	cfb := os.Getenv("COLORFGBG")
	if cfb == "" {
		return true
	}
	parts := strings.Split(cfb, ";")
	if len(parts) < 2 {
		return true
	}
	bg, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return true
	}
	return bg < 8
}

func init() {
	if TerminalIsDark() {
		bgThinking = "\033[48;2;18;40;18m"
	} else {
		bgThinking = "\033[48;2;248;253;248m"
	}
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

	text = strings.ReplaceAll(text, "\n", "\n"+clearLine)
	if !d.thinkingStarted {
		if StderrOutput {
			fmt.Fprintf(os.Stderr, "\n")
		}
		StderrOutput = true
		fmt.Fprintf(os.Stderr, "%s%s💭 %s", bgThinking, clearLine, text)
		d.thinkingStarted = true
	} else {
		fmt.Fprintf(os.Stderr, "%s", text)
	}
	d.thinkingActive = true
}

func (d *DebugHandler) EndThinking() {
	if !d.HideThinking && d.thinkingActive {
		fmt.Fprintf(os.Stderr, "%s%s\n\n", clearLine, bgReset)
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
