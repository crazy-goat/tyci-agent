package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/decodo/tyci/session"
	"github.com/spf13/cobra"
)

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Manage recorded sessions",
	Long: `Manage recorded session files.

Sessions are append-only JSONL files stored under ~/.tyci/sessions/<encoded-cwd>/
(encoded cwd = cwd with "/" replaced by "--", empty becomes "root").

Examples:
  tyci session list
  tyci session list --cwd .
  tyci session show <index>
  tyci session show <path>
  tyci session delete <index|path>
`,
}

var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recorded sessions for a directory",
	RunE:  runSessionList,
}

var sessionShowCmd = &cobra.Command{
	Use:   "show <index|path>",
	Short: "Print metadata for one session",
	Args:  cobra.ExactArgs(1),
	RunE:  runSessionShow,
}

var sessionDeleteCmd = &cobra.Command{
	Use:   "delete <index|path>",
	Short: "Delete a session file",
	Args:  cobra.ExactArgs(1),
	RunE:  runSessionDelete,
}

func init() {
	sessionListCmd.Flags().StringP("cwd", "C", ".", "Directory whose sessions to list")
	sessionShowCmd.Flags().StringP("cwd", "C", ".", "Directory whose sessions to search")
	sessionDeleteCmd.Flags().StringP("cwd", "C", ".", "Directory whose sessions to search")

	sessionCmd.AddCommand(sessionListCmd)
	sessionCmd.AddCommand(sessionShowCmd)
	sessionCmd.AddCommand(sessionDeleteCmd)
}

func resolveSessionRef(cwdFlag, ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("empty reference")
	}
	// Direct path: file exists or path-like (contains "/" or ends with .jsonl).
	if strings.ContainsRune(ref, os.PathSeparator) || strings.HasSuffix(ref, ".jsonl") {
		abs, err := filepath.Abs(ref)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(abs); err == nil {
			return abs, nil
		}
		// Fall through to try-as-index below for friendlier error.
	}

	idx, err := strconv.Atoi(ref)
	if err == nil {
		entries, lerr := listSessionEntries(cwdFlag)
		if lerr != nil {
			return "", lerr
		}
		if idx < 1 || idx > len(entries) {
			return "", fmt.Errorf("index %d out of range (1..%d)", idx, len(entries))
		}
		return entries[idx-1].Path, nil
	}

	return "", fmt.Errorf("not a path and not a numeric index: %q", ref)
}

func listSessionEntries(cwdFlag string) ([]session.SessionEntry, error) {
	abs, err := filepath.Abs(cwdFlag)
	if err != nil {
		return nil, err
	}
	dir, err := session.SessionDir(abs)
	if err != nil {
		return nil, err
	}
	return session.ListEntries(dir)
}

func runSessionList(cmd *cobra.Command, args []string) error {
	cwdFlag, _ := cmd.Flags().GetString("cwd")
	entries, err := listSessionEntries(cwdFlag)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		dir, _ := session.SessionDir(mustAbs(cwdFlag))
		fmt.Fprintf(os.Stdout, "No sessions in %s\n", dir)
		return nil
	}
	dir, _ := session.SessionDir(mustAbs(cwdFlag))
	fmt.Fprintf(os.Stdout, "Sessions in %s\n\n", dir)
	fmt.Fprintf(os.Stdout, "  %4s  %-20s  %10s  %s\n", "#", "modified (UTC)", "size", "path")
	for i, e := range entries {
		fmt.Fprintf(os.Stdout, "  %4d  %-20s  %10s  %s\n",
			i+1,
			e.ModTime.UTC().Format("2006-01-02 15:04:05"),
			humanBytes(e.Size),
			e.Path,
		)
	}
	return nil
}

func runSessionShow(cmd *cobra.Command, args []string) error {
	cwdFlag, _ := cmd.Flags().GetString("cwd")
	path, err := resolveSessionRef(cwdFlag, args[0])
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, _ := f.Stat()
	fmt.Fprintf(os.Stdout, "Path:    %s\n", path)
	if info != nil {
		fmt.Fprintf(os.Stdout, "Size:    %s\n", humanBytes(info.Size()))
		fmt.Fprintf(os.Stdout, "Modified: %s\n", info.ModTime().UTC().Format(time.RFC3339))
	}

	// Read just enough to dump headers / counts; tolerates large files.
	// The event "type" values and field names here must match what
	// session.Session actually writes (see session/session.go): the header
	// line has type "session", not "header", and tool activity lives in a
	// message event's Content blocks (type "toolCall"), not a top-level
	// "blocks" field. Decoding into the session package's structs (instead
	// of ad-hoc map access) keeps this in sync with that format.
	//
	// json.Decoder does not resynchronize after a syntax error — it keeps
	// returning the same error on every subsequent Decode, so a "skip one
	// bad line and keep going" loop built on it never advances and hangs
	// forever. A truncated last line (tyci killed mid-write) hits the same
	// trap via io.ErrUnexpectedEOF, which isn't io.EOF. Read line-by-line
	// with bufio.Scanner instead, exactly like session.parseSessionFile
	// (session/session.go) already does, so a bad line is skipped and the
	// scan moves on to the next one.
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var header *session.Header
	var messages, compactions, toolCalls int
	for scanner.Scan() {
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		var typed struct {
			Type session.EventType `json:"type"`
		}
		if err := json.Unmarshal(raw, &typed); err != nil {
			continue
		}
		switch typed.Type {
		case session.TypeSession:
			if header == nil {
				var h session.Header
				if err := json.Unmarshal(raw, &h); err == nil {
					header = &h
				}
			}
		case session.TypeMessage:
			messages++
			var msg session.MessageEvent
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			for _, b := range msg.Message.Content {
				// Only "toolCall" blocks exist in practice: a tool result is
				// a message event whose ROLE is "toolResult" but whose block
				// Type is "text" (see agent/session_log.go's
				// writeToolResultSessionEvent and run_once.go's own block
				// construction, which never writes Type "toolResult").
				// Counting a "toolResult" block type here would double-count
				// every tool call the day someone "fixes" that block type to
				// match its role.
				if b.Type == "toolCall" {
					toolCalls++
				}
			}
		case session.TypeCompaction:
			compactions++
		}
	}
	if header != nil {
		fmt.Fprintf(os.Stdout, "ID:      %s\n", header.ID)
		if header.CWD != "" {
			fmt.Fprintf(os.Stdout, "CWD:     %s\n", header.CWD)
		}
		if header.Model != "" {
			fmt.Fprintf(os.Stdout, "Model:   %s\n", header.Model)
		}
		if header.Provider != "" {
			fmt.Fprintf(os.Stdout, "Provider: %s\n", header.Provider)
		}
		if header.Timestamp != "" {
			fmt.Fprintf(os.Stdout, "Started: %s\n", header.Timestamp)
		}
	}
	// scanner.Err() is nil on a clean io.EOF but non-nil on anything that
	// stopped the scan early — most notably bufio.ErrTooLong when a line
	// exceeds the 8 MB buffer above, which does NOT resynchronize: Scan()
	// just returns false and every remaining line in the file is silently
	// unread. Without this check, a session with one huge line (or any
	// mid-read I/O error) would print undercounted Counts and exit 0 with
	// nothing to say the report is incomplete. session.parseSessionFile
	// (session/session.go) returns this same error rather than swallowing
	// it; here there is already a partial report worth keeping, so the
	// scan error is surfaced as a warning line instead of aborting before
	// any output at all.
	if scanErr := scanner.Err(); scanErr != nil {
		fmt.Fprintf(os.Stdout, "Warning: stopped reading early (%v); counts below are incomplete\n", scanErr)
	}
	fmt.Fprintf(os.Stdout, "Counts:  %d messages, %d tool calls, %d compactions\n",
		messages, toolCalls, compactions)
	return nil
}

func runSessionDelete(cmd *cobra.Command, args []string) error {
	cwdFlag, _ := cmd.Flags().GetString("cwd")
	path, err := resolveSessionRef(cwdFlag, args[0])
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Delete %s? [y/N] ", path)
	var ans string
	fmt.Scanln(&ans)
	ans = strings.ToLower(strings.TrimSpace(ans))
	if ans != "y" && ans != "yes" {
		fmt.Fprintln(os.Stdout, "Aborted.")
		return nil
	}
	if err := session.DeleteSession(path); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Deleted %s\n", path)
	return nil
}

func mustAbs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}

func humanBytes(n int64) string {
	const k = 1024
	if n < k {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"K", "M", "G", "T"}
	f := float64(n)
	i := 0
	for f >= k && i < len(units)-1 {
		f /= k
		i++
	}
	return fmt.Sprintf("%.1f %sB", f, units[i-1])
}
