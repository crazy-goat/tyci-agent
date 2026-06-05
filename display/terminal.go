package display

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	clearLine = "\033[K"
	bgReset   = "\033[0m"
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

type Terminal struct {
	hideThinking bool
	hideTools    bool
	interactive  bool
	bgTools      string
	bgThinking   string
	bgUsage      string

	thinkingActive  bool
	thinkingStarted bool
	sawStdout       bool
	textEndsNewline bool

	pendingTools []ToolCall
	currentTool  *ToolCall
}

func NewTerminal(hideThinking, hideTools bool, interactive bool) *Terminal {
	t := &Terminal{
		hideThinking: hideThinking,
		hideTools:    hideTools,
		interactive:  interactive,
	}
	if TerminalIsDark() {
		t.bgTools = "\033[48;2;18;18;42m"
		t.bgThinking = "\033[48;2;18;40;18m"
		t.bgUsage = "\033[48;2;70;70;70m"
	} else {
		t.bgTools = "\033[48;2;248;248;254m"
		t.bgThinking = "\033[48;2;248;253;248m"
		t.bgUsage = "\033[48;2;230;230;230m"
	}
	return t
}

func (t *Terminal) Chunk(text string) {
	fmt.Fprint(os.Stdout, text)
	_ = os.Stdout.Sync()
	t.sawStdout = true
	t.textEndsNewline = strings.HasSuffix(text, "\n")
}

func (t *Terminal) Thinking(text string) {
	if t.hideThinking {
		return
	}
	text = strings.ReplaceAll(text, "\n", "\n"+clearLine)
	if !t.thinkingStarted {
		fmt.Fprintf(os.Stderr, "%s%s💭 %s%s", t.bgThinking, clearLine, text, clearLine)
		t.thinkingStarted = true
	} else {
		fmt.Fprintf(os.Stderr, "%s%s%s", clearLine, text, clearLine)
	}
	t.thinkingActive = true
}

func (t *Terminal) EndThinking() {
	if !t.hideThinking && t.thinkingActive {
		fmt.Fprintf(os.Stderr, "%s%s\n\n", clearLine, bgReset)
		t.thinkingActive = false
		t.thinkingStarted = false
	}
}

func (t *Terminal) ToolCallStart(name string) {
	t.currentTool = &ToolCall{Name: name}
}

func (t *Terminal) ToolCallArg(text string) {
	if t.currentTool == nil {
		return
	}
	t.currentTool.Arguments += text
}

func (t *Terminal) EndToolCall() {
	if t.currentTool == nil {
		return
	}
	t.pendingTools = append(t.pendingTools, *t.currentTool)
	t.currentTool = nil
}

func (t *Terminal) ToolResult(name string, result *ToolResult) {
	if t.hideTools {
		t.pendingTools = t.pendingTools[:0]
		return
	}

	if len(t.pendingTools) == 0 {
		return
	}
	tc := t.pendingTools[0]
	t.pendingTools = t.pendingTools[1:]

	var block strings.Builder
	if tc.Name == "read" {
		block.WriteString(fmt.Sprintf("🔧 %s(%s):", tc.Name, tc.Arguments))
	} else {
		parsedArgs := parseArgs(tc.Arguments)
		title, _ := parsedArgs["description"].(string)
		if title != "" {
			cmd, _ := parsedArgs["command"].(string)
			if cmd != "" {
				block.WriteString(fmt.Sprintf("🔧 %s\n$ %s", title, cmd))
			} else {
				block.WriteString(fmt.Sprintf("🔧 %s", title))
			}
		} else {
			block.WriteString(fmt.Sprintf("🔧 %s(%s):", tc.Name, tc.Arguments))
		}

		if result != nil {
			block.WriteByte('\n')
			if result.Success {
				block.WriteString(result.Content)
			} else {
				block.WriteString(result.Error)
			}
		}
	}

	content := block.String()
	if t.interactive {
		content = strings.ReplaceAll(content, "\n", "\n"+clearLine)
		fmt.Fprintf(os.Stderr, "%s%s%s%s%s\n\n", t.bgTools, clearLine, content, clearLine, bgReset)
	} else {
		fmt.Fprintf(os.Stderr, "%s%s%s%s\n", t.bgTools, clearLine, content, bgReset)
	}
}

func (t *Terminal) Summary(usage UsageInfo) {
	newIn := usage.InputTokens - usage.CacheReadInputTokens
	if newIn < 0 {
		newIn = 0
	}
	parts := fmt.Sprintf("Usage: in=%d out=%d", newIn, usage.OutputTokens)
	if usage.CacheReadInputTokens > 0 || usage.CacheCreateInputTokens > 0 {
		parts += fmt.Sprintf(" cache_rd=%d cache_wr=%d", usage.CacheReadInputTokens, usage.CacheCreateInputTokens)
	}
	if usage.StopReason != "" {
		parts += fmt.Sprintf(" stop_reason=%s", usage.StopReason)
	}
	if t.interactive {
		if t.sawStdout && !t.textEndsNewline {
			fmt.Fprintln(os.Stderr)
		}
		if t.sawStdout {
			fmt.Fprintln(os.Stderr)
		}
		fmt.Fprintf(os.Stderr, "%s%s%s%s\n\n", t.bgUsage, clearLine, parts, bgReset)
	} else {
		fmt.Fprintf(os.Stderr, "%s%s\n", clearLine, parts)
	}
	t.sawStdout = false
	t.textEndsNewline = false
}

func (t *Terminal) Error(err error) {
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
}

func (t *Terminal) End() {}

func parseArgs(arguments string) map[string]any {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(arguments), &parsed); err != nil {
		return nil
	}
	return parsed
}
