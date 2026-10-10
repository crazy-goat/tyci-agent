package cron

import (
	"os"
	"strings"
	"testing"
	"time"
)

var reminderNow = time.Date(2026, 10, 9, 10, 0, 0, 0, time.Local)

func TestParseOnce_Forms(t *testing.T) {
	cases := map[string]time.Time{
		"in 20m":                    reminderNow.Add(20 * time.Minute),
		"IN 20M":                    reminderNow.Add(20 * time.Minute),
		"at 15:00":                  time.Date(2026, 10, 9, 15, 0, 0, 0, time.Local),
		"once 2026-10-09T12:00:00Z": time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC),
	}
	for in, want := range cases {
		got, err := ParseOnce(in, reminderNow)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if !got.Equal(want) {
			t.Errorf("%q: got %v, want %v", in, got, want)
		}
	}
}

func TestParseOnce_AtTomorrowWhenTimePassed(t *testing.T) {
	got, err := ParseOnce("at 09:00", reminderNow)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 10, 10, 9, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseOnce_RejectsRepeating(t *testing.T) {
	for _, in := range []string{"every 30m", "@daily 07:30", "07:30", "30m", ""} {
		_, err := ParseOnce(in, reminderNow)
		if err == nil {
			t.Errorf("%q: want an error", in)
			continue
		}
		if !strings.Contains(err.Error(), "use cron for repeating schedules") {
			t.Errorf("%q: error %q does not point to cron", in, err)
		}
	}
}

func TestParseOnce_RejectsZeroDuration(t *testing.T) {
	if _, err := ParseOnce("in 0s", reminderNow); err == nil {
		t.Error("in 0s: want an error")
	}
}

func TestReminderStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	f := &ReminderFile{}
	fires := reminderNow.Add(time.Hour)
	r, err := f.Add("check the PR", "/work/repo", fires, reminderNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveReminders(dir, f); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadReminders(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Reminders) != 1 {
		t.Fatalf("got %d reminders, want 1", len(loaded.Reminders))
	}
	got := loaded.Reminders[0]
	if got.ID != r.ID || got.Text != "check the PR" || got.Dir != "/work/repo" || !got.FiresAt.Equal(fires) {
		t.Errorf("round trip changed the reminder: %+v", got)
	}
}

func TestReminderStore_MissingFileIsEmpty(t *testing.T) {
	f, err := LoadReminders(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Reminders) != 0 {
		t.Errorf("got %d reminders, want 0", len(f.Reminders))
	}
}

func TestReminderDue_FiresAtNowIsDue(t *testing.T) {
	f := &ReminderFile{}
	if _, err := f.Add("now", "", reminderNow.Add(time.Second), reminderNow); err != nil {
		t.Fatal(err)
	}
	if got := f.Due(reminderNow); len(got) != 0 {
		t.Errorf("one second early: got %d due, want 0", len(got))
	}
	if got := f.Due(reminderNow.Add(time.Second)); len(got) != 1 {
		t.Errorf("at FiresAt: got %d due, want 1", len(got))
	}
}

func TestReminderDue_DeliveredIsNotDue(t *testing.T) {
	f := &ReminderFile{}
	if _, err := f.Add("done", "", reminderNow.Add(time.Minute), reminderNow); err != nil {
		t.Fatal(err)
	}
	f.Reminders[0].Delivered = true
	if got := f.Due(reminderNow.Add(time.Hour)); len(got) != 0 {
		t.Errorf("got %d due, want 0", len(got))
	}
}

func TestReminderSave_LeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	if err := SaveReminders(dir, &ReminderFile{}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ReminderFileName+".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestReminderRemove_UnknownIsFalse(t *testing.T) {
	f := &ReminderFile{}
	if f.Remove("nope") {
		t.Error("Remove of an unknown id returned true")
	}
}

func TestReminderAdd_RejectsEmptyTextAndPastTime(t *testing.T) {
	f := &ReminderFile{}
	if _, err := f.Add("   ", "", reminderNow.Add(time.Hour), reminderNow); err == nil || !strings.Contains(err.Error(), "a text is required") {
		t.Errorf("empty text: got %v", err)
	}
	if _, err := f.Add("late", "", reminderNow.Add(-time.Hour), reminderNow); err == nil || !strings.Contains(err.Error(), "the time is in the past") {
		t.Errorf("past time: got %v", err)
	}
	if len(f.Reminders) != 0 {
		t.Errorf("rejected reminders were stored: %d", len(f.Reminders))
	}
}
