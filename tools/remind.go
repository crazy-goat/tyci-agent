package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/cron"
)

// remindMu guards every load-modify-save of reminders.json in this process.
var remindMu sync.Mutex

// remindLockName is the lock file that lets only one process deliver reminders
// at a time. It is separate from the cron lock, so a long cron run does not
// block delivery.
const remindLockName = "reminders.lock"

// RemindTool sets, lists and cancels one-shot reminders for the orchestrator.
// A due reminder arrives as a notice, which wakes an idle chat.
type RemindTool struct{}

func (t *RemindTool) Name() string { return "remind" }

func (t *RemindTool) Run(ctx context.Context, input map[string]any) ToolResult {
	action := strings.TrimSpace(stringParam(input, "action", ""))
	switch action {
	case "add":
		return t.add(ctx, input)
	case "list":
		return t.list()
	case "cancel":
		return t.cancel(input)
	default:
		return failf("unknown action %q; use \"add\", \"list\" or \"cancel\"", action)
	}
}

func (t *RemindTool) add(ctx context.Context, input map[string]any) ToolResult {
	when := strings.TrimSpace(stringParam(input, "when", ""))
	text := strings.TrimSpace(stringParam(input, "text", ""))
	if when == "" {
		return failf("when is required: in <duration>, at HH:MM or once <RFC3339>")
	}
	if text == "" {
		return failf("text is required: what the reminder says")
	}
	now := time.Now()
	firesAt, err := cron.ParseOnce(when, now)
	if err != nil {
		return failf("%v", err)
	}
	dir := Workdir(ctx)
	if dir == "" {
		dir, _ = os.Getwd()
	}

	remindMu.Lock()
	defer remindMu.Unlock()
	f, err := cron.LoadReminders(cronConfigDir())
	if err != nil {
		return failf("%v", err)
	}
	r, err := f.Add(text, dir, firesAt, now)
	if err != nil {
		return failf("%v", err)
	}
	if err := cron.SaveReminders(cronConfigDir(), f); err != nil {
		return failf("%v", err)
	}
	out := map[string]any{"id": r.ID, "fires_at": r.FiresAt.Format(time.RFC3339), "text": r.Text}
	if !CronTickerRunning() {
		out["warning"] = "No scheduler is running in this process, so the reminder fires only in an interactive session."
	}
	return remindJSON(out)
}

func (t *RemindTool) list() ToolResult {
	remindMu.Lock()
	defer remindMu.Unlock()
	f, err := cron.LoadReminders(cronConfigDir())
	if err != nil {
		return failf("%v", err)
	}
	rows := make([]map[string]any, 0, len(f.Reminders))
	for _, r := range f.Reminders {
		if r.Delivered {
			continue
		}
		rows = append(rows, map[string]any{
			"id":       r.ID,
			"fires_at": r.FiresAt.Format(time.RFC3339),
			"text":     r.Text,
			"dir":      r.Dir,
		})
	}
	return remindJSON(rows)
}

func (t *RemindTool) cancel(input map[string]any) ToolResult {
	id := strings.TrimSpace(stringParam(input, "id", ""))
	if id == "" {
		return failf("id is required for action=\"cancel\"")
	}

	remindMu.Lock()
	defer remindMu.Unlock()
	f, err := cron.LoadReminders(cronConfigDir())
	if err != nil {
		return failf("%v", err)
	}
	if !f.Remove(id) {
		return failf("no reminder with id %q; list them with remind(action=\"list\")", id)
	}
	if err := cron.SaveReminders(cronConfigDir(), f); err != nil {
		return failf("%v", err)
	}
	return remindJSON(map[string]any{"id": id, "cancelled": true})
}

func remindJSON(v any) ToolResult {
	b, err := json.Marshal(v)
	if err != nil {
		return failf("remind: %v", err)
	}
	return okf("%s", b)
}

// deliverReminders sends every due reminder through notify. Each reminder is
// marked delivered and saved before notify runs, so a crash cannot send it
// twice. It returns how many notices it sent.
func deliverReminders(configDir string, now time.Time, notify func(string)) (int, error) {
	msgs, err := claimDueReminders(configDir, now)
	if err != nil {
		return 0, err
	}
	for _, m := range msgs {
		notify(m)
	}
	return len(msgs), nil
}

// claimDueReminders marks the due reminders delivered and returns their notice
// texts. Locks are released before the caller sends anything.
func claimDueReminders(configDir string, now time.Time) ([]string, error) {
	remindMu.Lock()
	defer remindMu.Unlock()

	release, ok, err := cron.TryLock(filepath.Join(configDir, remindLockName))
	defer release()
	if err != nil || !ok {
		return nil, err
	}

	f, err := cron.LoadReminders(configDir)
	if err != nil {
		return nil, err
	}
	due := f.Due(now)
	if len(due) == 0 {
		return nil, nil
	}
	cwd, _ := os.Getwd()
	msgs := make([]string, 0, len(due))
	for _, d := range due {
		for i := range f.Reminders {
			if f.Reminders[i].ID == d.ID {
				f.Reminders[i].Delivered = true
			}
		}
		msgs = append(msgs, reminderNotice(d, now, cwd))
	}
	if err := cron.SaveReminders(configDir, f); err != nil {
		return nil, err
	}
	return msgs, nil
}

// reminderNotice is the text of one notice. A reminder that is more than two
// minutes late says how late, and one set in another directory names it.
func reminderNotice(r cron.Reminder, now time.Time, cwd string) string {
	msg := fmt.Sprintf("reminder %s: %s", r.ID, r.Text)
	if late := now.Sub(r.FiresAt); late > 2*time.Minute {
		// "3h12m0s" is shown as "3h12m".
		msg += fmt.Sprintf(" (late by %s)", strings.TrimSuffix(late.Round(time.Minute).String(), "0s"))
	}
	if r.Dir != cwd {
		msg += fmt.Sprintf(" (set in %s)", r.Dir)
	}
	return msg
}
