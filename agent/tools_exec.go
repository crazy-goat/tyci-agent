package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/decodo/tyci/stream"
)

func executeTools(ctx context.Context, runner ToolRunner, toolCalls []stream.ToolCall) []string {
	results := make([]string, len(toolCalls))
	var wg sync.WaitGroup

	for i, tc := range toolCalls {
		wg.Add(1)
		go func(idx int, call stream.ToolCall) {
			defer wg.Done()

			var args map[string]any
			if call.Arguments != "" {
				if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
					results[idx] = "Error: invalid arguments: " + err.Error()
					return
				}
			}
			if args == nil {
				args = make(map[string]any)
			}

			// Determine timeout per tool type
			var toolTimeout time.Duration
			switch call.Name {
			case "read", "write":
				toolTimeout = 30 * time.Second
			case "bash":
				toolTimeout = 120 * time.Second // default
				if to, ok := args["timeout"]; ok {
					switch v := to.(type) {
					case float64:
						toolTimeout = time.Duration(v) * time.Second
					case int:
						toolTimeout = time.Duration(v) * time.Second
					}
				}
			case "subagent":
				toolTimeout = 0 // no timeout — subagent has its own internal timeout
			default:
				toolTimeout = 60 * time.Second
			}

			// Create tool-specific context with timeout (if set)
			toolCtx := ctx
			var cancel context.CancelFunc
			if toolTimeout > 0 {
				toolCtx, cancel = context.WithTimeout(ctx, toolTimeout)
				defer cancel()
			}

			// Pass tool index for streaming tools (bash, subagent)
			if call.Name == "bash" || call.Name == "subagent" {
				toolCtx = context.WithValue(toolCtx, stream.ToolIdxCtxKey{}, idx)
			}

			body, err := runner.Run(toolCtx, call.Name, args)
			if err != nil {
				// Check the actual context state – the returned error may have lost its type
				// after passing through tool wrappers (fmt.Errorf etc.).
				if toolCtx.Err() == context.DeadlineExceeded {
					results[idx] = fmt.Sprintf("Error: %s tool timed out after %v", call.Name, toolTimeout)
				} else {
					results[idx] = "Error: " + err.Error()
				}
			} else {
				results[idx] = body
			}
		}(i, tc)
	}

	wg.Wait()
	return results
}
