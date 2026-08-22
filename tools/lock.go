package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/decodo/tyci/locks"
)

// newHolderID returns a short random identifier used to label a lock's
// holder when the caller doesn't provide one. It has no relation to any
// particular agent/session identity in the tools package (there isn't one
// available here) — it's just a token the model is expected to remember and
// pass back to "unlock" later. Returned to the caller via ToolResult.Content.
func newHolderID() string {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("holder-%d", time.Now().UnixNano())
	}
	return "holder-" + hex.EncodeToString(buf[:])
}

// LockTool implements the "lock" tool: an advisory, in-process claim on a
// path so parallel subagents can agree not to step on each other's edits.
type LockTool struct {
	Registry *locks.Registry
}

func (t *LockTool) Name() string { return "lock" }

func (t *LockTool) Run(ctx context.Context, input map[string]any) ToolResult {
	if t.Registry == nil {
		return ToolResult{Type: "result", Success: false, Error: "lock tool has no registry configured"}
	}

	path, _ := input["path"].(string)
	if path == "" {
		return ToolResult{Type: "result", Success: false, Error: "path is required"}
	}

	var ttl time.Duration
	if raw, ok := input["seconds"]; ok && raw != nil {
		secs, err := toInt(raw)
		if err != nil {
			return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("invalid seconds: %v", err)}
		}
		if secs < 0 {
			return ToolResult{Type: "result", Success: false, Error: "seconds must be >= 0"}
		}
		ttl = time.Duration(secs) * time.Second
	}

	holder := newHolderID()

	_, ok, existing := t.Registry.Acquire(ctx, path, holder, ttl)
	if !ok {
		expiry := "no expiry"
		if existing != nil && !existing.ExpiresAt.IsZero() {
			expiry = "expires " + existing.ExpiresAt.Format(time.RFC3339)
		}
		return ToolResult{
			Type:    "result",
			Success: false,
			Error: fmt.Sprintf(
				"path %q already locked by %q since %s (%s). Do not edit it anyway — pick up something else and come back to it, or wait for the holder to unlock.",
				path, existing.Holder, existing.AcquiredAt.Format(time.RFC3339), expiry,
			),
		}
	}

	lifetime := "until you release it or your session ends"
	if ttl > 0 {
		lifetime = fmt.Sprintf("for %s", ttl)
	}
	return ToolResult{
		Type:    "result",
		Success: true,
		Content: fmt.Sprintf("locked %q as holder %q (%s). Remember this holder id — you need it to call unlock.", path, holder, lifetime),
	}
}

// UnlockTool implements the "unlock" tool: releases a path lock, but only
// when the caller supplies the exact holder id the "lock" tool returned.
type UnlockTool struct {
	Registry *locks.Registry
}

func (t *UnlockTool) Name() string { return "unlock" }

func (t *UnlockTool) Run(ctx context.Context, input map[string]any) ToolResult {
	if t.Registry == nil {
		return ToolResult{Type: "result", Success: false, Error: "unlock tool has no registry configured"}
	}

	path, _ := input["path"].(string)
	if path == "" {
		return ToolResult{Type: "result", Success: false, Error: "path is required"}
	}
	holder, _ := input["holder"].(string)
	if holder == "" {
		return ToolResult{Type: "result", Success: false, Error: "holder is required"}
	}

	if !t.Registry.Release(path, holder) {
		return ToolResult{
			Type:    "result",
			Success: false,
			Error:   fmt.Sprintf("could not unlock %q: not locked, already expired, or held by a different holder", path),
		}
	}

	return ToolResult{Type: "result", Success: true, Content: fmt.Sprintf("unlocked %q", path)}
}
