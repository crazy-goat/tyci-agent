package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/cron"
)

// useTempHome points HOME at a fresh directory, so the reminder file is not
// read from or written to the machine running the tests.
func useTempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func TestRemindCLI_ListAndCancel(t *testing.T) {
	useTempHome(t)
	dir, err := cronConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	fires := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	f := &cron.ReminderFile{Reminders: []cron.Reminder{{ID: "abcd1234", Text: "check the PR", Dir: "/tmp/p", FiresAt: fires}}}
	if err := cron.SaveReminders(dir, f); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	remindListCmd.SetOut(&out)
	if err := remindListCmd.RunE(remindListCmd, nil); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "abcd1234") || !strings.Contains(got, "check the PR") {
		t.Fatalf("list output %q lacks the id or the text", got)
	}

	out.Reset()
	remindCancelCmd.SetOut(&out)
	if err := remindCancelCmd.RunE(remindCancelCmd, []string{"abcd1234"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "cancelled abcd1234\n" {
		t.Fatalf("cancel output %q", got)
	}

	out.Reset()
	remindListCmd.SetOut(&out)
	if err := remindListCmd.RunE(remindListCmd, nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "no reminders" {
		t.Fatalf("list after cancel = %q, want no reminders", got)
	}
}

func TestRemindCancel_UnknownID(t *testing.T) {
	useTempHome(t)
	err := remindCancelCmd.RunE(remindCancelCmd, []string{"nope"})
	if err == nil || !strings.Contains(err.Error(), `no reminder with id "nope"`) {
		t.Fatalf("err = %v, want unknown id error", err)
	}
}

func TestFormatReminders_SkipsDelivered(t *testing.T) {
	at := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	lines := formatReminders([]cron.Reminder{
		{ID: "11111111", Text: "done", FiresAt: at, Delivered: true},
		{ID: "22222222", Text: "open", FiresAt: at},
	})
	if len(lines) != 1 || !strings.Contains(lines[0], "22222222") {
		t.Fatalf("lines = %q, want only the undelivered reminder", lines)
	}
}

func TestFormatReminders_KeepsFileOrder(t *testing.T) {
	late := time.Date(2026, 10, 10, 15, 0, 0, 0, time.UTC)
	early := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	lines := formatReminders([]cron.Reminder{
		{ID: "aaaaaaaa", Text: "late", FiresAt: late},
		{ID: "bbbbbbbb", Text: "early", FiresAt: early},
	})
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "aaaaaaaa") || !strings.HasPrefix(lines[1], "bbbbbbbb") {
		t.Fatalf("lines = %q, want file order", lines)
	}
}

func TestReminderListText_NoneIsMessage(t *testing.T) {
	useTempHome(t)
	got, err := reminderListText()
	if err != nil {
		t.Fatal(err)
	}
	if got != "no reminders" {
		t.Fatalf("got %q, want no reminders", got)
	}
}

func TestRemindersSlash_PrintsList(t *testing.T) {
	useTempHome(t)
	dir, err := cronConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	f := &cron.ReminderFile{Reminders: []cron.Reminder{{ID: "cafe0001", Text: "ping", FiresAt: time.Now().Add(time.Hour)}}}
	if err := cron.SaveReminders(dir, f); err != nil {
		t.Fatal(err)
	}
	var disp fakeSlashDisplay
	handleRemindersCommand(&disp)
	if !strings.Contains(disp.lastToolBlock, "cafe0001") {
		t.Fatalf("tool block = %q, want the reminder", disp.lastToolBlock)
	}
	if !disp.resetStatusCalled() {
		t.Fatal("ResetStatus was not called")
	}
}
