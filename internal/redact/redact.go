// Package redact removes secrets from text. It is best effort: it knows the
// common token shapes and the exact secrets registered with Add.
package redact

import (
	"regexp"
	"strings"
	"sync"
)

const mask = "[REDACTED]"

// minSecret is the shortest exact secret Add accepts; shorter values would
// replace ordinary words.
const minSecret = 8

var (
	mu      sync.RWMutex
	exact   []string
	pattern = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{50,})\b|` +
		`\bsk-[A-Za-z0-9_\-]{20,}|` +
		`\bAKIA[0-9A-Z]{16}\b|` +
		`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`)
	bearer = regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)[A-Za-z0-9._\-]+`)
	envVar = regexp.MustCompile(`(?i)\b([A-Z0-9_]*(?:TOKEN|SECRET|PASSWORD|API_?KEY)[A-Z0-9_]*)(\\?"?\s*[=:]\s*)(\\?["']?)[^\s"',;\\]{6,}`)
)

// Add registers an exact secret. Values shorter than 8 bytes are ignored.
// It is safe to call from many goroutines.
func Add(secret string) {
	if len(secret) < minSecret {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	for _, s := range exact {
		if s == secret {
			return
		}
	}
	exact = append(exact, secret)
}

// Redact returns s with secrets replaced by "[REDACTED]".
func Redact(s string) string {
	mu.RLock()
	for _, x := range exact {
		s = strings.ReplaceAll(s, x, mask)
	}
	mu.RUnlock()
	s = pattern.ReplaceAllString(s, mask)
	s = bearer.ReplaceAllString(s, "${1}"+mask)
	return envVar.ReplaceAllString(s, "${1}${2}${3}"+mask)
}
