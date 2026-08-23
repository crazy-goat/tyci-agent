package cron

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FileName is where the jobs live, under the same ~/.tyci directory as the
// rest of tyci's global state.
const FileName = "cron.json"

// LogDirName holds one log file per job, so a run nobody watched can still be
// read afterwards. Without it a scheduled prompt writes its output to a
// terminal that is not there.
const LogDirName = "cron-logs"

// maxLogBytes caps one job's log. A prompt that runs every ten minutes for a
// month would otherwise quietly fill the disk. The newest half is kept: what
// went wrong last night matters more than what went right in July.
const maxLogBytes = 1 << 20

// Job is one saved prompt and when to run it.
type Job struct {
	Name     string `json:"name"`
	Prompt   string `json:"prompt"`
	Dir      string `json:"dir"`
	Model    string `json:"model,omitempty"`
	Schedule string `json:"schedule"`
	Disabled bool   `json:"disabled,omitempty"`

	// LastRun/LastStatus are written back after each run, which is what makes
	// the schedule survive a restart instead of firing again immediately.
	LastRun    time.Time `json:"last_run,omitempty"`
	LastStatus string    `json:"last_status,omitempty"`
}

// Parsed returns the job's schedule, or an error naming the job so a typo in
// one entry is traceable.
func (j Job) Parsed() (Schedule, error) {
	s, err := ParseSchedule(j.Schedule)
	if err != nil {
		return Schedule{}, fmt.Errorf("job %q: %w", j.Name, err)
	}
	return s, nil
}

// File is the on-disk document.
type File struct {
	Jobs []Job `json:"jobs"`
}

// Path returns the jobs file inside dir (the ~/.tyci directory).
func Path(configDir string) string { return filepath.Join(configDir, FileName) }

// LogPath returns the log file for one job.
func LogPath(configDir, name string) string {
	return filepath.Join(configDir, LogDirName, SanitizeName(name)+".log")
}

// SanitizeName makes a job name safe as a file name, and is also what Add
// validates against: a name with a slash in it would write its log outside the
// log directory.
func SanitizeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "job"
	}
	return out
}

// Load reads the jobs file. A missing file is not an error: it means no jobs.
func Load(configDir string) (*File, error) {
	data, err := os.ReadFile(Path(configDir))
	if os.IsNotExist(err) {
		return &File{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cron: read %s: %w", Path(configDir), err)
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("cron: %s is not valid JSON: %w", Path(configDir), err)
	}
	return &f, nil
}

// LoadMerged reads the jobs file from each dir in order and unions the
// result, later dirs winning on a same-name collision (case-insensitive,
// matching Find) — the same union-with-later-wins shape mcp.json and
// skills/ already use for their project-local override (TODO.md item 22).
// A trusted caller passes [globalDir, projectLocalDir]; an untrusted one, or
// one that never resolved a project dir, passes just [globalDir] and gets
// exactly Load's result. The merged list is sorted by name, same as Save
// leaves it.
//
// This is a read-side merge only: Save, MarkRun and LogPath still operate
// on one dir at a time. Callers that mutate a specific job (enable, disable,
// remove, and MarkRun after a run) must first find which dir currently
// defines it — see FindJobDir — since a job may live in the project-local
// file instead of the global one.
func LoadMerged(dirs ...string) (*File, error) {
	byKey := map[string]Job{}
	var order []string
	for _, dir := range dirs {
		f, err := Load(dir)
		if err != nil {
			return nil, err
		}
		for _, j := range f.Jobs {
			key := strings.ToLower(j.Name)
			if _, seen := byKey[key]; !seen {
				order = append(order, key)
			}
			byKey[key] = j
		}
	}
	merged := &File{}
	for _, key := range order {
		merged.Jobs = append(merged.Jobs, byKey[key])
	}
	sort.SliceStable(merged.Jobs, func(i, j int) bool { return merged.Jobs[i].Name < merged.Jobs[j].Name })
	return merged, nil
}

// FindJobDir reports which of dirs currently defines a job named name,
// preferring the LAST dir that does (so with dirs ordered
// [globalDir, projectLocalDir] a job present in both is reported as living
// in the project-local one — the same file LoadMerged would have taken its
// definition from). Returns ok=false when no dir in the list defines it.
func FindJobDir(dirs []string, name string) (dir string, ok bool) {
	for i := len(dirs) - 1; i >= 0; i-- {
		f, err := Load(dirs[i])
		if err != nil {
			continue
		}
		if f.Find(name) >= 0 {
			return dirs[i], true
		}
	}
	return "", false
}

// Save writes the jobs file, replacing it atomically so a crash mid-write
// cannot leave a half-file that loses every job.
func Save(configDir string, f *File) error {
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("cron: %w", err)
	}
	sort.SliceStable(f.Jobs, func(i, j int) bool { return f.Jobs[i].Name < f.Jobs[j].Name })
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("cron: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(configDir, FileName+".tmp*")
	if err != nil {
		return fmt.Errorf("cron: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("cron: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("cron: %w", err)
	}
	return os.Rename(tmp.Name(), Path(configDir))
}

// Find returns the index of the named job, or -1. Names are matched
// case-insensitively: they are typed by hand on a command line.
func (f *File) Find(name string) int {
	for i, j := range f.Jobs {
		if strings.EqualFold(j.Name, name) {
			return i
		}
	}
	return -1
}

// Add appends a job, rejecting a duplicate name rather than silently creating
// a second job that looks identical in `cron list`.
func (f *File) Add(j Job) error {
	if strings.TrimSpace(j.Name) == "" {
		return fmt.Errorf("cron: a name is required (it identifies the job in cron list/rm and names its log)")
	}
	if strings.TrimSpace(j.Prompt) == "" {
		return fmt.Errorf("cron: a prompt is required (it is what the agent will be asked to do)")
	}
	if _, err := ParseSchedule(j.Schedule); err != nil {
		return err
	}
	if f.Find(j.Name) >= 0 {
		return fmt.Errorf("cron: a job named %q already exists (remove it first, or pick another name)", j.Name)
	}
	f.Jobs = append(f.Jobs, j)
	return nil
}

// Remove deletes the named job. Returns false when there was nothing to
// delete, so the caller can say so instead of reporting a success.
func (f *File) Remove(name string) bool {
	i := f.Find(name)
	if i < 0 {
		return false
	}
	f.Jobs = append(f.Jobs[:i], f.Jobs[i+1:]...)
	return true
}

// Due returns the jobs that should run at now, in file order. A job whose
// schedule does not parse is skipped rather than fatal: one bad entry must not
// stop every other job from running.
func (f *File) Due(now time.Time) []Job {
	var out []Job
	for _, j := range f.Jobs {
		if j.Disabled {
			continue
		}
		s, err := j.Parsed()
		if err != nil {
			continue
		}
		if s.Due(now, j.LastRun) {
			out = append(out, j)
		}
	}
	return out
}

// Broken returns one error per job whose schedule does not parse, so the
// caller can report what is being skipped instead of leaving it invisible.
func (f *File) Broken() []error {
	var errs []error
	for _, j := range f.Jobs {
		if _, err := j.Parsed(); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// MarkRun records the outcome of a run and persists it. Reloads the file first
// so a run started minutes ago does not overwrite a job added since.
func MarkRun(configDir, name string, at time.Time, status string) error {
	f, err := Load(configDir)
	if err != nil {
		return err
	}
	i := f.Find(name)
	if i < 0 {
		// The job was removed while it was running. Nothing to record.
		return nil
	}
	f.Jobs[i].LastRun = at
	f.Jobs[i].LastStatus = status
	return Save(configDir, f)
}

// TrimLog keeps a job's log under maxLogBytes by dropping the older half.
func TrimLog(path string) error {
	info, err := os.Stat(path)
	if err != nil || info.Size() <= maxLogBytes {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	keep := data[len(data)-maxLogBytes/2:]
	// Start at a line boundary so the first surviving line is not a fragment.
	if i := strings.IndexByte(string(keep), '\n'); i >= 0 {
		keep = keep[i+1:]
	}
	header := []byte("--- older entries trimmed ---\n")
	return os.WriteFile(path, append(header, keep...), 0o644)
}
