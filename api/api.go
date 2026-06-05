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

	"github.com/decodo/tyci-agent/display"
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
	fmt.Fprintf(os.Stdout, "%s — %s\n", prefix, reason)
	for remaining > 0 {
		time.Sleep(1 * time.Second)
		remaining--
		if remaining > 0 {
			fmt.Fprintf(os.Stdout, "\r%s — %s — next in %ds... ", prefix, reason, remaining)
		}
	}
	fmt.Fprintf(os.Stdout, "\r%s — %s — retrying...                    \n", prefix, reason)
}

type UsageInfo struct {
	InputTokens           int `json:"prompt_tokens"`
	InputTokensAlt        int `json:"input_tokens,omitempty"`
	OutputTokens          int `json:"completion_tokens"`
	OutputTokensAlt       int `json:"output_tokens,omitempty"`
	ReasoningTokens       int `json:"reasoning_tokens,omitempty"`
	CacheHitTokens        int `json:"prompt_cache_hit_tokens,omitempty"`
	CacheMissTokens       int `json:"prompt_cache_miss_tokens,omitempty"`
	CacheReadInputTokens  int `json:"cache_read_input_tokens,omitempty"`
	CacheCreateInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

type ToolCall struct {
	Index    int
	Name     string
	Argument string
}

type DebugHandler struct {
	Inner           display.Display
	Debug           bool
	ToolCalls       []ToolCall
	FinishReason    string
	sawDone         bool
	InputTokens     int
	OutputTokens    int
	ReasoningTokens int
	CacheRead       int
	CacheWrite      int
}

func (d *DebugHandler) Chunk(text string) {
	d.Inner.Chunk(text)
	if d.Debug {
		fmt.Fprintf(os.Stdout, "[CHUNK] %s\n", text)
	}
}

func (d *DebugHandler) Thinking(text string) {
	d.Inner.Thinking(text)
	if d.Debug {
		fmt.Fprintf(os.Stdout, "[THINKING] %s\n", text)
	}
}

func (d *DebugHandler) EndThinking() {
	d.Inner.EndThinking()
}

func (d *DebugHandler) LogToolCallStart(name string) {
	d.Inner.ToolCallStart(name)
}

func (d *DebugHandler) ToolCallArg(text string) {
	d.Inner.ToolCallArg(text)
}

func (d *DebugHandler) EndToolCall() {
	d.Inner.EndToolCall()
}

func (d *DebugHandler) Summary(usage UsageInfo) {
	in := usage.InputTokens
	if in == 0 {
		in = usage.InputTokensAlt
	}
	out := usage.OutputTokens
	if out == 0 {
		out = usage.OutputTokensAlt
	}
	cacheRead := usage.CacheReadInputTokens
	if cacheRead == 0 {
		cacheRead = usage.CacheHitTokens
	}
	cacheWrite := usage.CacheCreateInputTokens
	if cacheWrite == 0 {
		cacheWrite = usage.CacheMissTokens
	}
	d.Inner.Summary(display.UsageInfo{
		InputTokens:           in,
		OutputTokens:          out + usage.ReasoningTokens,
		CacheReadInputTokens:  cacheRead,
		CacheCreateInputTokens: cacheWrite,
		StopReason:            d.FinishReason,
	})
}

func (d *DebugHandler) End() {
}

func (d *DebugHandler) Error(err error) {
	d.Inner.Error(err)
	if d.Debug {
		fmt.Fprintf(os.Stdout, "[ERROR] %v\n", err)
	}
}

func (d *DebugHandler) LogRequest(method, url string, body any) {
	if !d.Debug {
		return
	}
	jsonBody, _ := json.Marshal(body)
	fmt.Fprintf(os.Stdout, "[DEBUG REQ] %s %s\n%s\n", method, url, string(jsonBody))
}

func (d *DebugHandler) LogResponse(data string) {
	if !d.Debug {
		return
	}
	fmt.Fprintf(os.Stdout, "[DEBUG RESP] %s\n", data)
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
		fmt.Fprintf(os.Stdout, "[TOOL_CALL] idx=%d name=%q arg_accumulated=%q\n", idx, d.ToolCalls[idx].Name, d.ToolCalls[idx].Argument)
	}
}

func (d *DebugHandler) GetToolCalls() []ToolCall {
	return d.ToolCalls
}

func (d *DebugHandler) GetFinishReason() string {
	return d.FinishReason
}

func (d *DebugHandler) SawDone() bool {
	return d.sawDone
}

func (d *DebugHandler) GetUsage() (input, output, reasoning int) {
	return d.InputTokens, d.OutputTokens, d.ReasoningTokens
}

func (d *DebugHandler) ResetToolCalls() {
	d.ToolCalls = nil
}
