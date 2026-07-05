package main

import (
	"encoding/json"
	"fmt"
	"io"
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
	dec := json.NewDecoder(f)
	var header map[string]any
	var messages, compactions, toolCalls int
	for {
		var raw map[string]any
		if err := dec.Decode(&raw); err != nil {
			if err == io.EOF {
				break
			}
			// Try to keep going on a single corrupt line.
			continue
		}
		t, _ := raw["type"].(string)
		switch t {
		case "header":
			if header == nil {
				header = raw
			}
		case "message":
			messages++
			if blocks, ok := raw["blocks"].([]any); ok {
				for _, b := range blocks {
					if bm, ok := b.(map[string]any); ok {
						if bm["type"] == "tool_use" || bm["type"] == "tool_result" {
							toolCalls++
						}
					}
				}
			}
		case "compaction":
			compactions++
		}
	}
	if header != nil {
		if id, ok := header["id"].(string); ok {
			fmt.Fprintf(os.Stdout, "ID:      %s\n", id)
		}
		if cwd, ok := header["cwd"].(string); ok {
			fmt.Fprintf(os.Stdout, "CWD:     %s\n", cwd)
		}
		if m, ok := header["model"].(string); ok {
			fmt.Fprintf(os.Stdout, "Model:   %s\n", m)
		}
		if p, ok := header["provider"].(string); ok {
			fmt.Fprintf(os.Stdout, "Provider:%s\n", p)
		}
		if ts, ok := header["timestamp"].(string); ok {
			fmt.Fprintf(os.Stdout, "Started: %s\n", ts)
		}
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
