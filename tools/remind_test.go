package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/cron"
)

func runRemind(t *testing.T, input map[string]any) ToolResult {
	t.Helper()
	return (&RemindTool{}).Run(context.Background(), input)
}

func TestRemind_AddListCancel(t *testing.T) {
	withCronHome(t)

	added := runRemind(t, map[string]any{"action": "add", "when": "in 20m", "text": "check PR"})
	if !added.Success {
		t.Fatalf("add failed: %s", added.Content)
	}
	if !strings.Contains(added.Content, `"text":"check PR"`) {
		t.Fatalf("add result lacks the text: %s", added.Content)
	}

	listed := runRemind(t, map[string]any{"action": "list"})
	if !strings.Contains(listed.Content, "check PR") {
		t.Fatalf("list lacks the reminder: %s", listed.Content)
	}

	f, err := cron.LoadReminders(cronConfigDir())
	if err != nil || len(f.Reminders) != 1 {
		t.Fatalf("want one stored reminder, got %v, err %v", f, err)
	}
	cancelled := runRemind(t, map[string]any{"action": "cancel", "id": f.Reminders[0].ID})
	if !cancelled.Success {
		t.Fatalf("cancel failed: %s", cancelled.Content)
	}

	if got := runRemind(t, map[string]any{"action": "list"}).Content; got != "[]" {
		t.Fatalf("list after cancel = %q, want []", got)
	}
}

func TestRemind_CancelUnknownSaysWhatToDo(t *testing.T) {
	withCronHome(t)

	res := runRemind(t, map[string]any{"action": "cancel", "id": "deadbeef"})
	if res.Success {
		t.Fatal("cancel of an unknown id succeeded")
	}
	if !strings.Contains(res.Error, `remind(action="list")`) {
		t.Fatalf("failure does not say what to do: %s", res.Error)
	}
}

func TestRemind_AddRejectsRepeatingWhen(t *testing.T) {
	withCronHome(t)

	res := runRemind(t, map[string]any{"action": "add", "when": "every 30m", "text": "x"})
	if res.Success {
		t.Fatal("a repeating when was accepted")
	}
	if !strings.Contains(res.Error, "use cron for repeating schedules") {
		t.Fatalf("failure does not point to cron: %s", res.Error)
	}
}

func TestRemind_UnknownActionListsRealOnes(t *testing.T) {
	res := runRemind(t, map[string]any{"action": "snooze"})
	if res.Success {
		t.Fatal("unknown action succeeded")
	}
	for _, want := range []string{`"add"`, `"list"`, `"cancel"`} {
		if !strings.Contains(res.Error, want) {
			t.Fatalf("failure lacks %s: %s", want, res.Error)
		}
	}
}

func TestRemind_SubagentDoesNotHaveRemind(t *testing.T) {
	if !IsSubagentDenied("remind") {
		t.Fatal("remind is not denied to subagents")
	}
}

func writeDueReminder(t *testing.T, dir string, firesAt time.Time) string {
	t.Helper()
	f, err := cron.LoadReminders(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := firesAt.Add(-time.Hour)
	r, err := f.Add("stand up", "/proj", firesAt, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := cron.SaveReminders(dir, f); err != nil {
		t.Fatal(err)
	}
	return r.ID
}

func TestDeliverReminders_OneNoticeThenNoRepeat(t *testing.T) {
	withCronHome(t)
	now := time.Now()
	writeDueReminder(t, cronConfigDir(), now)

	var got []string
	notify := func(msg string) { got = append(got, msg) }
	if n, err := deliverReminders(cronConfigDir(), now, notify); err != nil || n != 1 {
		t.Fatalf("first delivery = %d, %v; want 1, nil", n, err)
	}
	if n, err := deliverReminders(cronConfigDir(), now, notify); err != nil || n != 0 {
		t.Fatalf("second delivery = %d, %v; want 0, nil", n, err)
	}
	if len(got) != 1 {
		t.Fatalf("notices = %d, want 1", len(got))
	}
}

func TestDeliverReminders_NotDueYet(t *testing.T) {
	withCronHome(t)
	now := time.Now()
	writeDueReminder(t, cronConfigDir(), now.Add(time.Hour))

	n, err := deliverReminders(cronConfigDir(), now, func(string) { t.Error("notice sent before the reminder is due") })
	if err != nil || n != 0 {
		t.Fatalf("delivery = %d, %v; want 0, nil", n, err)
	}
}

func TestDeliverReminders_LateMarker(t *testing.T) {
	withCronHome(t)
	now := time.Now()
	writeDueReminder(t, cronConfigDir(), now.Add(-(3*time.Hour + 12*time.Minute)))

	var got string
	if _, err := deliverReminders(cronConfigDir(), now, func(msg string) { got = msg }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "(late by 3h12m)") {
		t.Fatalf("notice lacks the late marker: %q", got)
	}
}

func TestDeliverReminders_MarksBeforeNotify(t *testing.T) {
	withCronHome(t)
	now := time.Now()
	writeDueReminder(t, cronConfigDir(), now)

	marked := false
	notify := func(string) {
		f, err := cron.LoadReminders(cronConfigDir())
		if err != nil {
			t.Fatal(err)
		}
		marked = len(f.Reminders) == 1 && f.Reminders[0].Delivered
	}
	if _, err := deliverReminders(cronConfigDir(), now, notify); err != nil {
		t.Fatal(err)
	}
	if !marked {
		t.Fatal("the reminder was not saved as delivered before notify ran")
	}
}

func TestDeliverReminders_SkipsWhenAnotherProcessHoldsLock(t *testing.T) {
	withCronHome(t)
	now := time.Now()
	writeDueReminder(t, cronConfigDir(), now)

	release, ok, err := cron.TryLock(filepath.Join(cronConfigDir(), remindLockName))
	if err != nil || !ok {
		t.Fatalf("taking the lock: ok=%v err=%v", ok, err)
	}
	defer release()

	n, err := deliverReminders(cronConfigDir(), now, func(string) { t.Error("notice sent while the lock is held") })
	if err != nil || n != 0 {
		t.Fatalf("delivery = %d, %v; want 0, nil", n, err)
	}
}
