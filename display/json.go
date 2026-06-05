package display

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/decodo/tyci-agent/stream"
)

type JSON struct {
	text      strings.Builder
	usage     *stream.Usage
	stopReason string
}

func NewJSON() *JSON {
	return &JSON{}
}

func (j *JSON) Thinking(text string) {}
func (j *JSON) ToolCall(name, args, result string) {}

func (j *JSON) Text(text string) {
	j.text.WriteString(text)
}

func (j *JSON) Summary(usage stream.Usage) {
	j.usage = &usage
}

func (j *JSON) Error(err error) {
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
}

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
		if j.usage != nil {
			output["usage"] = j.usage
		}
		jsonBytes, _ := json.MarshalIndent(output, "", "  ")
		fmt.Fprintln(os.Stdout, string(jsonBytes))
	} else {
		if j.usage != nil {
			output := map[string]interface{}{
				"response": jsonData,
				"usage":    j.usage,
			}
			jsonBytes, _ := json.MarshalIndent(output, "", "  ")
			fmt.Fprintln(os.Stdout, string(jsonBytes))
		} else {
			jsonBytes, _ := json.MarshalIndent(jsonData, "", "  ")
			fmt.Fprintln(os.Stdout, string(jsonBytes))
		}
	}
}

func (j *JSON) Text2() string {
	return j.text.String()
}
