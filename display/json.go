package display

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type JSON struct {
	text        strings.Builder
	toolCalls   []ToolCall
	currentTool *ToolCall
}

func NewJSON() *JSON {
	return &JSON{}
}

func (j *JSON) Chunk(text string) {
	j.text.WriteString(text)
}

func (j *JSON) Thinking(text string) {}

func (j *JSON) EndThinking() {}

func (j *JSON) ToolCallStart(name string) {
	j.currentTool = &ToolCall{Name: name}
}

func (j *JSON) ToolCallArg(text string) {
	if j.currentTool == nil {
		return
	}
	j.currentTool.Arguments += text
}

func (j *JSON) EndToolCall() {
	if j.currentTool == nil {
		return
	}
	j.toolCalls = append(j.toolCalls, *j.currentTool)
	j.currentTool = nil
}

func (j *JSON) ToolResult(name string, result *ToolResult) {}

func (j *JSON) Summary(usage UsageInfo) {}

func (j *JSON) Error(err error) {}

func (j *JSON) End() {
	responseText := j.text.String()
	if responseText == "" {
		return
	}
	var jsonData interface{}
	if err := json.Unmarshal([]byte(responseText), &jsonData); err != nil {
		output := map[string]interface{}{
			"response": responseText,
		}
		if len(j.toolCalls) > 0 {
			output["tool_calls"] = j.toolCalls
		}
		jsonBytes, _ := json.MarshalIndent(output, "", "  ")
		fmt.Fprintln(os.Stdout, string(jsonBytes))
	} else {
		jsonBytes, _ := json.MarshalIndent(jsonData, "", "  ")
		fmt.Fprintln(os.Stdout, string(jsonBytes))
	}
}

func (j *JSON) Text() string {
	return j.text.String()
}
