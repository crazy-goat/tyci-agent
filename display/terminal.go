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
	bgTools      string
	bgThinking   string

	thinkingActive  bool
	thinkingStarted bool
	sawStderr       bool
	toolCount       int

	pendingTools []ToolCall
	currentTool  *ToolCall
}

func NewTerminal(hideThinking, hideTools bool) *Terminal {
	t := &Terminal{
		hideThinking: hideThinking,
		hideTools:    hideTools,
	}
	if TerminalIsDark() {
		t.bgTools = "\033[48;2;18;18;42m"
		t.bgThinking = "\033[48;2;18;40;18m"
	} else {
		t.bgTools = "\033[48;2;248;248;254m"
		t.bgThinking = "\033[48;2;248;253;248m"
	}
	return t
}

func (t *Terminal) Chunk(text string) {
	fmt.Fprint(os.Stdout, text)
	_ = os.Stdout.Sync()
}

func (t *Terminal) Thinking(text string) {
	if t.hideThinking {
		return
	}
	text = strings.ReplaceAll(text, "\n", "\n"+clearLine)
	if !t.thinkingStarted {
		if t.sawStderr {
			fmt.Fprintf(os.Stderr, "\n")
		}
		t.sawStderr = true
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

	if t.toolCount > 0 {
		fmt.Fprintln(os.Stderr)
	}
	t.toolCount++

	if tc.Name == "read" {
		fmt.Fprintf(os.Stderr, "%s%s🔧 %s(%s):%s%s\n", t.bgTools, clearLine, tc.Name, tc.Arguments, clearLine, bgReset)
		t.sawStderr = true
		return
	}

	parsedArgs := parseArgs(tc.Arguments)
	title, _ := parsedArgs["description"].(string)
	if title != "" {
		cmd, _ := parsedArgs["command"].(string)
		if cmd != "" {
			fmt.Fprintf(os.Stderr, "%s%s🔧 %s\n%s$ %s\n", t.bgTools, clearLine, title, clearLine, cmd)
		} else {
			fmt.Fprintf(os.Stderr, "%s%s🔧 %s\n", t.bgTools, clearLine, title)
		}
	} else {
		fmt.Fprintf(os.Stderr, "%s%s🔧 %s(%s):\n", t.bgTools, clearLine, tc.Name, tc.Arguments)
	}

	if result != nil {
		if result.Success {
			content := strings.ReplaceAll(result.Content, "\n", "\n"+clearLine)
			fmt.Fprintf(os.Stderr, "%s%s%s%s\n", t.bgTools, content, clearLine, bgReset)
		} else {
			fmt.Fprintf(os.Stderr, "%s%s%s%s\n", t.bgTools, clearLine, result.Error, bgReset)
		}
	}
	t.sawStderr = true
}

func (t *Terminal) Summary(usage UsageInfo) {}

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
