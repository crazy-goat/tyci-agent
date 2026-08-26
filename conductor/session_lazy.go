package conductor

import (
	"fmt"
	"os"

	"github.com/decodo/tyci/session"
)

// ensureLazySession opens a session file at sessionPath if it isn't already
// open, returning the (possibly freshly-opened) *session.Session and the path
// for downstream writes. It is the single entry point used by console, TUI
// and one-shot run to optionally recreate their session the moment we have a
// user prompt to write — rather than at startup, which would otherwise litter
// ~/.tyci/sessions/ with empty JSONL files for every repl a user opens
// without ever typing a prompt.
//
// Behavior:
//   - If sess is already non-nil (e.g. --session explicitly resumed an
//     existing file from disk), it is returned as-is.
//   - If sessionPath is empty or --no-session is the reason we have no path,
//     it returns (nil, "", nil) so callsites can fall through to
//     "no-session" mode without any extra plumbing.
//   - Otherwise the path is opened. A file that already exists on disk is
//     resumed (same as session.Open behavior); a fresh file is created with a
//     header, exactly as if the user had passed --session up front.
//
// Errors from session.Open are reported on stderr and the function returns
// (nil, "", false, nil) so callers disable persistence for this session
// rather than crashing the REPL. This is the one place the conductor writes
// to a stream of its own instead of the Sink, and it is deliberate: it is a
// diagnostic about the log file, the same class as the "session write"
// warnings agent/session_log.go has always emitted, not a rendering decision.
//
// The returned bool is true only when this call is the one that actually
// opened the file (fresh or resumed) — the caller uses it to record the
// system_prompt ledger event exactly once per open, instead of re-reading
// the file on every Submit just to no-op against an already-open session.
func ensureLazySession(sess *session.Session, sessionPath, cwd, modelName, providerName string) (*session.Session, string, bool, error) {
	if sess != nil {
		return sess, sessionPath, false, nil
	}
	if sessionPath == "" {
		return nil, "", false, nil
	}
	resolvedCWD := normalizeCWD(cwd)
	newSess, err := session.Open(sessionPath, resolvedCWD, modelName, providerName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: session: %v (continuing without session)\n", err)
		return nil, "", false, nil
	}
	return newSess, sessionPath, true, nil
}

// normalizeCWD falls back to the current working directory if the supplied
// value is empty. Used by ensureLazySession so callers don't have to repeat
// the os.Getwd dance.
func normalizeCWD(cwd string) string {
	if cwd != "" {
		return cwd
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return ""
}
