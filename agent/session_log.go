package agent

import (
	"fmt"
	"os"
	"strings"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/session"
	"github.com/decodo/tyci/stream"
)

func writeAssistantSessionEvent(s *session.Session, providerName, model string, msg connector.Message, usage *stream.Usage) {
	// Convert connector.ContentBlock to session.ContentBlock (they have identical structure)
	blocks := make([]session.ContentBlock, len(msg.Content))
	for i, cb := range msg.Content {
		blocks[i] = session.ContentBlock{
			Type:       cb.Type,
			Text:       cb.Text,
			Thinking:   cb.Thinking,
			ID:         cb.ID,
			Name:       cb.Name,
			Arguments:  cb.Arguments,
			IsError:    cb.IsError,
			ToolCallID: cb.ToolCallID,
			ToolName:   cb.ToolName,
		}
	}

	opts := &session.MessageOptions{
		Provider: providerName,
		Model:    model,
	}
	if usage != nil {
		opts.Usage = &session.Usage{
			Input:       usage.Input,
			Output:      usage.Output,
			Reasoning:   usage.Reasoning,
			CacheRead:   usage.CacheRead,
			CacheWrite:  usage.CacheWrite,
			TotalTokens: usage.Input + usage.Output + usage.Reasoning,
		}
	}

	if err := s.WriteMessage("assistant", blocks, opts); err != nil {
		// Non-fatal: log but don't break agent
		fmt.Fprintf(os.Stderr, "Warning: session write (assistant): %v\n", err)
	}
}

func writeToolResultSessionEvent(s *session.Session, toolCallID, toolName, result string) {
	blocks := []session.ContentBlock{
		{
			Type:       "text",
			Text:       result,
			ToolCallID: toolCallID,
			ToolName:   toolName,
			IsError:    strings.HasPrefix(result, "Error:"),
		},
	}
	if err := s.WriteMessage("toolResult", blocks, nil); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: session write (toolResult): %v\n", err)
	}
}

// WriteSessionEnd writes a session_end event and closes the session.
// Safe to call multiple times; subsequent calls are no-ops.
func WriteSessionEnd(s *session.Session, status string, exitCode int, totalUsage *stream.Usage) {
	if s == nil {
		return
	}
	var u *session.Usage
	if totalUsage != nil {
		u = &session.Usage{
			Input:       totalUsage.Input,
			Output:      totalUsage.Output,
			Reasoning:   totalUsage.Reasoning,
			CacheRead:   totalUsage.CacheRead,
			CacheWrite:  totalUsage.CacheWrite,
			TotalTokens: totalUsage.Input + totalUsage.Output + totalUsage.Reasoning,
		}
	}
	if err := s.WriteSessionEnd(status, exitCode, u); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: session write (session_end): %v\n", err)
	}
	if err := s.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: session close: %v\n", err)
	}
}
