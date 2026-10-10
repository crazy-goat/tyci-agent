package cron

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ReminderFileName is where the reminders live, under the same ~/.tyci
// directory as cron.json. They have their own file: a reminder is a one-shot
// notice, and a cron job is a repeating prompt that runs.
const ReminderFileName = "reminders.json"

// Reminder is one one-shot notice for the orchestrator.
type Reminder struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	Dir       string    `json:"dir"`
	FiresAt   time.Time `json:"fires_at"`
	CreatedAt time.Time `json:"created_at"`
	Delivered bool      `json:"delivered,omitempty"`
}

// ReminderFile is the on-disk document of reminders.json.
type ReminderFile struct {
	Reminders []Reminder `json:"reminders"`
}

// ReminderPath returns the reminders file inside dir (the ~/.tyci directory).
func ReminderPath(configDir string) string {
	return filepath.Join(configDir, ReminderFileName)
}

// LoadReminders reads the reminders file. A missing file means no reminders.
func LoadReminders(configDir string) (*ReminderFile, error) {
	data, err := os.ReadFile(ReminderPath(configDir))
	if os.IsNotExist(err) {
		return &ReminderFile{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reminder: read %s: %w", ReminderPath(configDir), err)
	}
	var f ReminderFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("reminder: %s is not valid JSON: %w", ReminderPath(configDir), err)
	}
	return &f, nil
}

// SaveReminders writes the reminders file, replacing it atomically so a crash
// mid-write cannot leave a half-file that loses every reminder.
func SaveReminders(configDir string, f *ReminderFile) error {
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("reminder: %w", err)
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("reminder: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(configDir, ReminderFileName+".tmp*")
	if err != nil {
		return fmt.Errorf("reminder: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close() // the write error is what gets reported
		return fmt.Errorf("reminder: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("reminder: %w", err)
	}
	return os.Rename(tmp.Name(), ReminderPath(configDir))
}

// Add appends a reminder that fires at firesAt. It rejects an empty text and a
// time that is not after now.
func (f *ReminderFile) Add(text, dir string, firesAt, now time.Time) (Reminder, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Reminder{}, fmt.Errorf("reminder: a text is required")
	}
	if !firesAt.After(now) {
		return Reminder{}, fmt.Errorf("reminder: the time is in the past")
	}
	id, err := NewReminderID()
	if err != nil {
		return Reminder{}, err
	}
	r := Reminder{ID: id, Text: text, Dir: dir, FiresAt: firesAt, CreatedAt: now}
	f.Reminders = append(f.Reminders, r)
	return r, nil
}

// Remove deletes the reminder with the given id. Returns false when there was
// nothing to delete.
func (f *ReminderFile) Remove(id string) bool {
	for i, r := range f.Reminders {
		if r.ID == id {
			f.Reminders = append(f.Reminders[:i], f.Reminders[i+1:]...)
			return true
		}
	}
	return false
}

// Due returns the reminders that are not delivered yet and fire at or before
// now, in file order.
func (f *ReminderFile) Due(now time.Time) []Reminder {
	var out []Reminder
	for _, r := range f.Reminders {
		if !r.Delivered && !r.FiresAt.After(now) {
			out = append(out, r)
		}
	}
	return out
}

// NewReminderID returns a random id of 8 lowercase hex characters.
func NewReminderID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("reminder: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// ParseOnce resolves a one-shot time to an absolute moment. It accepts
// "in <duration>", "at HH:MM" (today, or tomorrow when that time has passed)
// and "once <RFC3339>". Repeating schedules are rejected: they belong to cron.
func ParseOnce(s string, now time.Time) (time.Time, error) {
	fail := fmt.Errorf("%q is not a one-shot time: use in <duration>, at HH:MM or once <RFC3339>; use cron for repeating schedules", s)

	kw, rest, ok := strings.Cut(strings.TrimSpace(s), " ")
	if !ok {
		return time.Time{}, fail
	}
	rest = strings.TrimSpace(rest)
	switch strings.ToLower(kw) {
	case "in":
		d, err := time.ParseDuration(strings.ToLower(rest))
		if err != nil || d <= 0 {
			return time.Time{}, fail
		}
		return now.Add(d), nil
	case "at":
		t, err := time.Parse("15:04", strings.ToLower(rest))
		if err != nil {
			return time.Time{}, fail
		}
		at := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
		if !at.After(now) {
			at = at.AddDate(0, 0, 1)
		}
		return at, nil
	case "once":
		t, err := time.Parse(time.RFC3339, rest)
		if err != nil {
			return time.Time{}, fail
		}
		return t, nil
	default:
		return time.Time{}, fail
	}
}
