// Package cron runs saved prompts on a schedule.
//
// It exists for the work nobody wants to remember to start: the nightly test
// run, the hourly check on a queue, the Monday morning summary. tyci already
// knows how to run one prompt to completion without a person watching
// (`tyci run --prompt`), so what was missing was only a record of what to run
// and when.
//
// Deliberately two small vocabularies rather than crontab's five fields:
// "every 30m" and "at 07:30". A crontab expression is a compact way to say
// something most schedules never need to say, and getting it subtly wrong is
// silent — the job simply never fires.
package cron

import (
	"fmt"
	"strings"
	"time"
)

// minInterval bounds "every". Below a minute the daemon would spend its life
// starting agents, and no useful prompt finishes that fast.
const minInterval = time.Minute

// Schedule is either an interval or a time of day. Exactly one is set.
type Schedule struct {
	// Every is the interval between runs, measured from the end of the last
	// one. Zero when this is a daily schedule.
	Every time.Duration
	// Hour and Minute are the local time of day for a daily schedule, used
	// only when Every is zero.
	Hour, Minute int
}

// ParseSchedule accepts "every <duration>" (e.g. "every 30m", "every 6h") or
// "at HH:MM" (local time, daily). "@daily 07:30" and a bare duration are
// accepted too, because they are the two things people type by accident.
func ParseSchedule(s string) (Schedule, error) {
	text := strings.TrimSpace(strings.ToLower(s))
	if text == "" {
		return Schedule{}, fmt.Errorf("schedule is required: \"every 30m\" or \"at 07:30\"")
	}
	switch {
	case strings.HasPrefix(text, "every "):
		return parseEvery(strings.TrimSpace(strings.TrimPrefix(text, "every ")))
	case strings.HasPrefix(text, "at "):
		return parseAt(strings.TrimSpace(strings.TrimPrefix(text, "at ")))
	case strings.HasPrefix(text, "@daily "):
		return parseAt(strings.TrimSpace(strings.TrimPrefix(text, "@daily ")))
	case strings.Contains(text, ":"):
		return parseAt(text)
	default:
		return parseEvery(text)
	}
}

func parseEvery(text string) (Schedule, error) {
	d, err := time.ParseDuration(text)
	if err != nil {
		return Schedule{}, fmt.Errorf("%q is not a duration: use e.g. \"every 30m\", \"every 6h\"", text)
	}
	if d < minInterval {
		return Schedule{}, fmt.Errorf("every %s is too often; the shortest interval is %s", text, minInterval)
	}
	return Schedule{Every: d}, nil
}

func parseAt(text string) (Schedule, error) {
	t, err := time.Parse("15:04", text)
	if err != nil {
		return Schedule{}, fmt.Errorf("%q is not a time of day: use 24-hour HH:MM, e.g. \"at 07:30\"", text)
	}
	return Schedule{Hour: t.Hour(), Minute: t.Minute()}, nil
}

// Daily reports whether this is a time-of-day schedule.
func (s Schedule) Daily() bool { return s.Every == 0 }

// String round-trips through ParseSchedule.
func (s Schedule) String() string {
	if s.Daily() {
		return fmt.Sprintf("at %02d:%02d", s.Hour, s.Minute)
	}
	return "every " + s.Every.String()
}

// Due reports whether a job last run at last should run now.
//
// A job that has never run is due at once, on purpose: the alternative is
// adding a nightly job and having no way to tell whether it works until
// tomorrow.
func (s Schedule) Due(now, last time.Time) bool {
	if !s.Daily() {
		return last.IsZero() || !now.Before(last.Add(s.Every))
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), s.Hour, s.Minute, 0, 0, now.Location())
	if now.Before(today) {
		// Before today's slot: due only if the last run predates yesterday's.
		return last.IsZero() || last.Before(today.AddDate(0, 0, -1))
	}
	return last.IsZero() || last.Before(today)
}

// Next returns when a job last run at last will run next. Used for listing, so
// a person can see whether "at 07:30" means this morning or tomorrow.
func (s Schedule) Next(now, last time.Time) time.Time {
	if s.Due(now, last) {
		return now
	}
	if !s.Daily() {
		return last.Add(s.Every)
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), s.Hour, s.Minute, 0, 0, now.Location())
	if now.Before(today) {
		return today
	}
	return today.AddDate(0, 0, 1)
}
