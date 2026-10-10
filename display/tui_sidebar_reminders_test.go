package display

import (
	"strings"
	"testing"
	"time"
)

func reminderLines(rows []sidebarTaskRow) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.line)
	}
	return out
}

func TestSidebarReminderRows_TextAndDueState(t *testing.T) {
	rows := sidebarReminderRows([]TuiReminderRow{
		{ID: "a", Text: "check   PR", FiresAt: time.Now().Add(12 * time.Minute)},
		{ID: "b", Text: "overdue", FiresAt: time.Now().Add(-time.Minute)},
	}, 80)
	lines := reminderLines(rows)
	if len(lines) != 3 || lines[0] != "Reminders" {
		t.Fatalf("rows = %q, want a heading and two reminders", lines)
	}
	if lines[1] != "reminder due: overdue" {
		t.Fatalf("first reminder = %q, want the due one first", lines[1])
	}
	if lines[2] != "reminder in 12m: check PR" {
		t.Fatalf("second reminder = %q", lines[2])
	}
}

func TestSidebarReminderRows_DoesNotSortCallerSlice(t *testing.T) {
	now := time.Now()
	in := []TuiReminderRow{
		{ID: "late", Text: "late", FiresAt: now.Add(2 * time.Hour)},
		{ID: "soon", Text: "soon", FiresAt: now.Add(time.Hour)},
	}
	_ = sidebarReminderRows(in, 80)
	if in[0].ID != "late" || in[1].ID != "soon" {
		t.Fatalf("caller slice was reordered: %v", in)
	}
}

func TestSidebarReminderRows_NoneNoGroup(t *testing.T) {
	if rows := sidebarReminderRows(nil, 80); rows != nil {
		t.Fatalf("rows = %v, want none", rows)
	}
}

func TestSidebarTasks_ShowsReminderRowNotSelectable(t *testing.T) {
	m := newTestModelForSidebar()
	m.reminderLister = func() []TuiReminderRow {
		return []TuiReminderRow{{ID: "a", Text: "check PR", FiresAt: time.Now().Add(12 * time.Minute)}}
	}
	m.openSidebar(sidebarTabTasks)
	rows := m.sidebarTaskRows(60)
	found := false
	for _, r := range rows {
		if strings.Contains(r.line, "reminder in 12m: check PR") {
			found = true
			if r.job != nil || r.isMain {
				t.Fatalf("reminder row must not be selectable: %+v", r)
			}
		}
	}
	if !found {
		t.Fatalf("no reminder row in %q", reminderLines(rows))
	}
	for _, i := range sidebarJobRowIndices(rows) {
		if strings.Contains(rows[i].line, "reminder") {
			t.Fatalf("reminder row %d is in the selectable indices", i)
		}
	}
}
