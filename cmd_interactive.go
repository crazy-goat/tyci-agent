package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/decodo/tyci/display"
	"github.com/decodo/tyci/internal/readline"
	"github.com/decodo/tyci/stream"
	"github.com/decodo/tyci/tools"
	"golang.org/x/term"
)

// collector captures agent output (simplified for subagent runner)
type collector struct {
	text strings.Builder
}

func (c *collector) Thinking(text string)            { c.text.WriteString(text) }
func (c *collector) Text(text string)                { c.text.WriteString(text) }
func (c *collector) Request(string)                  {}
func (c *collector) ToolCallStart(name string)       {}
func (c *collector) ToolCallDelta(delta string)      {}
func (c *collector) ToolCallEnd(name, result string) {}
func (c *collector) ToolFinish()                     {}
func (c *collector) ToolBlock(msg string)            {}
func (c *collector) Summary(usage stream.Usage, stats stream.Stats) {}
func (c *collector) Total(usage stream.Usage)                       {}
func (c *collector) Error(err error)                 {}
func (c *collector) End()                            {}

// toolsAdapter implements the tools.Runner interface by delegating to tools.RunTool.
type toolsAdapter struct{}

func (toolsAdapter) Run(ctx context.Context, name string, args map[string]any) (string, error) {
	res := tools.RunTool(ctx, name, args)
	if res.Success {
		return res.Content, nil
	}
	return "", fmt.Errorf("%s", res.Error)
}

var fallbackScanner *bufio.Scanner

// simplePrompt prompts the user for input using a basic scanner.
func simplePrompt(prompt string) (string, error) {
	if fallbackScanner == nil {
		fallbackScanner = bufio.NewScanner(os.Stdin)
	}
	fmt.Fprint(os.Stdout, prompt)
	if !fallbackScanner.Scan() {
		return "", readline.ErrEOF
	}
	return fallbackScanner.Text(), fallbackScanner.Err()
}

// watchESC watches for ESC key press to cancel the context.
func watchESC(cancel context.CancelFunc) func() {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return func() {}
	}

	oldState, err := term.GetState(fd)
	if err != nil {
		return func() {}
	}

	// Set raw mode (non-canonical, no echo, etc.)
	_, err = term.MakeRaw(fd)
	if err != nil {
		return func() {}
	}

	// Tweak: keep ISIG (for Ctrl+C signals) and OPOST (output processing),
	// set VMIN=0 VTIME=1 so read() returns every 100ms instead of blocking forever.
	if err := applyTerminalTweaks(fd); err != nil {
		term.Restore(fd, oldState)
		return func() {}
	}

	stop := make(chan struct{})
	go func() {
		buf := make([]byte, 1)
		for {
			select {
			case <-stop:
				return
			default:
			}
			n, err := os.Stdin.Read(buf)
			if err != nil || n == 0 {
				// timeout or error — check if we should stop before retrying
				select {
				case <-stop:
					return
				default:
					continue
				}
			}
			if buf[0] == 0x1b { // ESC
				cancel()
				return
			}
			// Any other key is discarded
		}
	}()

	return func() {
		close(stop) // signal goroutine to stop
		term.Restore(fd, oldState)
		// Don't wait for goroutine — it will exit on next timeout or stop signal
	}
}

// replaySessionToDisplay reads a JSONL session file and replays all events
// visually through the display, showing thinking, text, tool calls, results
// and usage as they happened during the original run.
func replaySessionToDisplay(disp display.Display, sessionPath string) {
	f, err := os.Open(sessionPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: cannot replay session: %v\n", err)
		return
	}
	defer f.Close()

	disp.ToolBlock("📋 Session history")
	disp.End()

	type pendingTool struct {
		id   string
		name string
	}
	var pendingTools []pendingTool

	var lastUsage stream.Usage
	var totalUsage stream.Usage
	hasUsage := false

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}

		evType, _ := raw["type"].(string)
		if evType == "session" || evType == "session_end" {
			continue
		}

		msgRaw, ok := raw["message"].(map[string]any)
		if !ok {
			continue
		}
		role, _ := msgRaw["role"].(string)
		content, _ := msgRaw["content"].([]any)

		switch role {
		case "user":
			// Show user prompt
			for _, block := range content {
				b, ok := block.(map[string]any)
				if !ok {
					continue
				}
				if txt, _ := b["text"].(string); txt != "" {
					disp.Text("User: " + txt)
				}
			}

		case "assistant":
			for _, block := range content {
				b, ok := block.(map[string]any)
				if !ok {
					continue
				}
				bType, _ := b["type"].(string)
				switch bType {
				case "thinking":
					if txt, _ := b["thinking"].(string); txt != "" {
						disp.Thinking(txt)
					}
				case "text":
					if txt, _ := b["text"].(string); txt != "" {
						disp.Text(txt)
					}
				case "toolCall":
					id, _ := b["id"].(string)
					name, _ := b["name"].(string)
					disp.ToolCallStart(name)
					if args, ok := b["arguments"]; ok {
						switch v := args.(type) {
						case string:
							if v != "" {
								disp.ToolCallDelta(v)
							}
						case map[string]any, []any:
							if data, err := json.Marshal(v); err == nil {
								disp.ToolCallDelta(string(data))
							}
						}
					}
					pendingTools = append(pendingTools, pendingTool{id: id, name: name})
				}
			}

			// Track usage
			if u, ok := raw["usage"].(map[string]any); ok {
				lastUsage = parseUsageFromMap(u)
				totalUsage.Add(lastUsage)
				hasUsage = true
			}

		case "toolResult", "tool":
			for _, block := range content {
				b, ok := block.(map[string]any)
				if !ok {
					continue
				}
				bType, _ := b["type"].(string)
				if bType != "text" {
					continue
				}
				txt, _ := b["text"].(string)
				toolName, _ := b["toolName"].(string)
				toolCallID, _ := b["toolCallId"].(string)

				// Match by toolCallId or name
				matched := false
				for i, pt := range pendingTools {
					if pt.id == toolCallID || pt.name == toolName {
						disp.ToolCallEnd(pt.name, txt)
						pendingTools = append(pendingTools[:i], pendingTools[i+1:]...)
						matched = true
						break
					}
				}
				if !matched && toolName != "" {
					disp.ToolCallEnd(toolName, txt)
				} else if !matched && len(pendingTools) > 0 {
					pt := pendingTools[0]
					disp.ToolCallEnd(pt.name, txt)
					pendingTools = pendingTools[1:]
				}
			}
		}
	}

	if hasUsage {
		disp.Summary(totalUsage, stream.Stats{})
	}

	// Separator before new output
	disp.End()
	disp.ToolBlock("▶️ Continuing from session end")
	disp.End()

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: session replay error: %v\n", err)
	}
}

// parseUsageFromMap extracts stream.Usage from a JSON map.
func parseUsageFromMap(u map[string]any) stream.Usage {
	var us stream.Usage
	if v, ok := u["input"].(float64); ok {
		us.Input = int(v)
	}
	if v, ok := u["output"].(float64); ok {
		us.Output = int(v)
	}
	if v, ok := u["reasoning"].(float64); ok {
		us.Reasoning = int(v)
	}
	if v, ok := u["cacheRead"].(float64); ok {
		us.CacheRead = int(v)
	}
	if v, ok := u["cacheWrite"].(float64); ok {
		us.CacheWrite = int(v)
	}
	return us
}
