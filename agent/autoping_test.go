package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/connector/connectortest"
	"github.com/crazy-goat/tyci-agent/jobs"
	"github.com/crazy-goat/tyci-agent/stream"
)

// clockRunner advances a fake clock by 100 s on every tool call.
type clockRunner struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clockRunner) Run(ctx context.Context, name string, args map[string]any) (string, error) {
	c.mu.Lock()
	c.now = c.now.Add(100 * time.Second)
	c.mu.Unlock()
	return "ok", nil
}

func (c *clockRunner) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func TestAgentLoop_AutoPingAfterIgnoredNudge(t *testing.T) {
	clock := &clockRunner{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	reg := jobs.NewRegistry()
	reg.SetClockForTests(clock.Now)
	release := make(chan struct{})
	defer close(release)
	job := reg.Start(context.Background(), "silent", jobs.KindSubagent, "", func(ctx context.Context, id string) (string, bool, error) {
		<-release
		return "", false, nil
	})

	const interval = 5 * time.Minute
	var events []string
	cfg := Config{
		MaxRetries:    1,
		MaxIterations: 6,
		Tools:         clock,
		ProgressHeartbeat: func() bool {
			if reg.NeedsProgressHeartbeat(job.ID, interval/2) {
				events = append(events, "nudge")
				return true
			}
			return false
		},
		AutoPing: func(last string) {
			if reg.AutoProgress(job.ID, interval, last) {
				events = append(events, "auto:"+last)
			}
		},
	}
	fake := &connectortest.Fake{
		OnExhausted: []stream.Event{
			stream.ToolCall{ID: "tc", Name: "bash", Arguments: `{"command":"go test ./..."}`},
			stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
		},
	}
	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}}}
	_, _ = Run(context.Background(), fake, &silentDisplay{}, &msgs, cfg)

	autos := 0
	for _, e := range events {
		if strings.HasPrefix(e, "auto:") {
			autos++
			if e != "auto:bash: go test ./..." {
				t.Fatalf("bad summary %q", e)
			}
		}
	}
	if autos != 1 {
		t.Fatalf("want exactly one auto note, events %v", events)
	}
	if len(events) == 0 || events[0] != "nudge" {
		t.Fatalf("the nudge must come first, events %v", events)
	}
}

func TestLastToolSummary(t *testing.T) {
	long := strings.Repeat("x", 200)
	msgs := []connector.Message{{Role: "assistant", Content: []connector.ContentBlock{
		{Type: "toolCall", Name: "edit", Arguments: []byte(`{"path":"tools/foo.go","new":"` + long + `"}`)},
	}}}
	if got := lastToolSummary(msgs); got != "edit: tools/foo.go" {
		t.Fatalf("got %q", got)
	}
	msgs[0].Content[0] = connector.ContentBlock{Type: "toolCall", Name: "bash", Arguments: []byte(`{"command":"` + long + `"}`)}
	if got := lastToolSummary(msgs); len([]rune(got)) != len("bash: ")+80 {
		t.Fatalf("not cut to 80 runes: %q", got)
	}
	if lastToolSummary(nil) != "" {
		t.Fatal("expected empty summary")
	}
}
