package display

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/decodo/tyci-agent/stream"
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

type blockKind int

const (
	blockNone blockKind = iota
	blockThinking
	blockText
	blockTool
	blockUsage
)

type Terminal struct {
	bgTools    string
	bgThinking string
	bgUsage    string

	curBlock blockKind
	curBg    string
}

func NewTerminal() *Terminal {
	t := &Terminal{}
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

func (t *Terminal) continueBlock(kind blockKind, bg string) bool {
	if t.curBlock == kind {
		return false
	}
	t.newBlock(kind, bg)
	return true
}

func (t *Terminal) newBlock(kind blockKind, bg string) {
	t.closeBlock()
	t.curBlock = kind
	t.curBg = bg
	if bg != "" {
		fmt.Fprint(os.Stdout, bg)
		fmt.Fprint(os.Stdout, clearLine) // wypełnij całą pierwszą linię tłem
	}
}

func (t *Terminal) closeBlock() {
	if t.curBlock == blockNone {
		return
	}
	if t.curBg != "" {
		fmt.Fprint(os.Stdout, clearLine) // wypełnij resztę linii tłem
		fmt.Fprint(os.Stdout, bgReset)
	}
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout)
	t.curBlock = blockNone
	t.curBg = ""
}

func (t *Terminal) Thinking(text string) {
	// ensure each new line gets filled with background
	text = strings.ReplaceAll(text, "\n", "\n"+clearLine)
	if t.continueBlock(blockThinking, t.bgThinking) {
		fmt.Fprintf(os.Stdout, "💭 %s", text)
		return
	}
	fmt.Fprint(os.Stdout, text)
}

func (t *Terminal) Text(text string) {
	t.continueBlock(blockText, "")
	fmt.Fprint(os.Stdout, text)
}

func (t *Terminal) ToolCall(name, args, result string) {
	t.newBlock(blockTool, t.bgTools)

	var block strings.Builder
	parsedArgs := parseArgs(args)
	title, _ := parsedArgs["description"].(string)
	if title != "" {
		cmd, _ := parsedArgs["command"].(string)
		if cmd != "" {
			block.WriteString(fmt.Sprintf("🔧 %s\n$ %s", title, cmd))
		} else {
			block.WriteString(fmt.Sprintf("🔧 %s", title))
		}
	} else {
		block.WriteString(fmt.Sprintf("🔧 %s(%s)", name, args))
	}

	if name == "read" {
	} else if result != "" {
		block.WriteByte('\n')
		block.WriteString(result)
	}

	// ensure each new line gets filled with background
	out := strings.ReplaceAll(block.String(), "\n", "\n"+clearLine)
	fmt.Fprint(os.Stdout, out)
}

func (t *Terminal) Summary(usage stream.Usage, stats stream.Stats) {
	t.newBlock(blockUsage, t.bgUsage)
	fmt.Fprintf(os.Stdout, "Usage: %s", buildUsageLine(usage, stats))
}

func (t *Terminal) Error(err error) {
	t.closeBlock()
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
}

func (t *Terminal) End() {
	t.closeBlock()
}

func parseArgs(arguments string) map[string]any {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(arguments), &parsed); err != nil {
		return nil
	}
	return parsed
}
