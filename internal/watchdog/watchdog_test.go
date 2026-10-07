package watchdog

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/jobs"
)

type sent struct{ to, text string }

type rig struct {
	t     *testing.T
	reg   *jobs.Registry
	w     *Watchdog
	msgs  []sent
	gone  map[string]bool // ids whose Notify fails
	stop  chan struct{}
	start time.Time
}

func newRig(t *testing.T) *rig {
	r := &rig{t: t, reg: jobs.NewRegistry(), gone: map[string]bool{}, stop: make(chan struct{})}
	r.w = &Watchdog{Reg: r.reg, IdleAfter: 3 * time.Minute, EscalateAfter: 3 * time.Minute,
		Notify: func(to, text string) bool {
			if r.gone[to] {
				return false
			}
			r.msgs = append(r.msgs, sent{to, text})
			return true
		}}
	t.Cleanup(func() { close(r.stop) })
	return r
}

func (r *rig) job(kind jobs.Kind, parent string) jobs.Job {
	j := r.reg.Start(context.Background(), "worker", kind, parent, func(ctx context.Context, _ string) (string, bool, error) {
		<-r.stop
		return "", false, nil
	})
	got, _ := r.reg.Get(j.ID)
	r.start = got.StartedAt
	return got.Snapshot()
}

func (r *rig) tick(after time.Duration) {
	r.msgs = nil
	r.w.Tick(r.start.Add(after))
}

func TestTick_NoActivityBelowThreshold(t *testing.T) {
	r := newRig(t)
	r.job(jobs.KindSubagent, "")
	r.tick(2*time.Minute + 59*time.Second)
	if len(r.msgs) != 0 {
		t.Fatalf("got %v", r.msgs)
	}
}

func TestTick_PingsParentOnce(t *testing.T) {
	r := newRig(t)
	p := r.job(jobs.KindOther, "")
	r.job(jobs.KindSubagent, p.ID)
	r.reg.TouchActivity(p.ID)
	r.tick(3 * time.Minute)
	if len(r.msgs) != 1 || r.msgs[0].to != p.ID {
		t.Fatalf("got %v", r.msgs)
	}
	r.tick(3*time.Minute + 15*time.Second)
	if len(r.msgs) != 0 {
		t.Fatalf("second tick sent %v", r.msgs)
	}
}

func TestTick_EscalatesToGrandparentAndHumanOnce(t *testing.T) {
	r := newRig(t)
	gp := r.job(jobs.KindOther, "")
	p := r.job(jobs.KindOther, gp.ID)
	r.job(jobs.KindSubagent, p.ID)
	r.tick(3 * time.Minute)
	if len(r.msgs) != 1 || r.msgs[0].to != p.ID {
		t.Fatalf("level 1: %v", r.msgs)
	}
	r.tick(6 * time.Minute)
	if len(r.msgs) != 1 || r.msgs[0].to != gp.ID {
		t.Fatalf("level 2: %v", r.msgs)
	}
	r.tick(9 * time.Minute)
	if len(r.msgs) != 1 || r.msgs[0].to != "" {
		t.Fatalf("human: %v", r.msgs)
	}
	r.tick(5 * time.Hour)
	if len(r.msgs) != 0 {
		t.Fatalf("second human message: %v", r.msgs)
	}
}

func TestTick_PostFalseEscalatesImmediately(t *testing.T) {
	r := newRig(t)
	gp := r.job(jobs.KindOther, "")
	p := r.job(jobs.KindOther, gp.ID)
	r.job(jobs.KindSubagent, p.ID)
	r.gone[p.ID] = true
	r.tick(3 * time.Minute)
	if len(r.msgs) != 1 || r.msgs[0].to != gp.ID {
		t.Fatalf("got %v", r.msgs)
	}
}

func TestTick_TopLevelParentNotifiesHumanImmediately(t *testing.T) {
	r := newRig(t)
	r.job(jobs.KindSubagent, "")
	r.tick(3 * time.Minute)
	if len(r.msgs) != 1 || r.msgs[0].to != "" {
		t.Fatalf("got %v", r.msgs)
	}
	r.tick(10 * time.Minute)
	if len(r.msgs) != 0 {
		t.Fatalf("repeat: %v", r.msgs)
	}
}

func TestTick_ActivityResetsLadder(t *testing.T) {
	r := newRig(t)
	p := r.job(jobs.KindOther, "")
	c := r.job(jobs.KindSubagent, p.ID)
	r.tick(3 * time.Minute)
	if len(r.msgs) != 1 {
		t.Fatalf("got %v", r.msgs)
	}
	r.reg.TouchActivity(c.ID) // real now: far in the fake future's past
	r.w.Tick(time.Now().Add(time.Second))
	if len(r.w.state) != 0 {
		t.Fatalf("state not reset: %v", r.w.state)
	}
	r.msgs = nil
	r.w.Tick(time.Now().Add(3 * time.Minute))
	if len(r.msgs) != 1 || r.msgs[0].to != p.ID {
		t.Fatalf("new period: %v", r.msgs)
	}
}

func TestTick_IgnoresNonSubagentAndTerminal(t *testing.T) {
	r := newRig(t)
	r.job(jobs.KindBash, "")
	done := r.reg.Start(context.Background(), "d", jobs.KindSubagent, "", func(context.Context, string) (string, bool, error) {
		return "", false, nil
	})
	_, _ = r.reg.Wait(context.Background(), done.ID, time.Second)
	r.tick(time.Hour)
	if len(r.msgs) != 0 {
		t.Fatalf("got %v", r.msgs)
	}
}

func TestTick_MessageUsesRealIdleTime(t *testing.T) {
	r := newRig(t)
	p := r.job(jobs.KindOther, "")
	r.job(jobs.KindSubagent, p.ID)
	r.tick(4*time.Minute + 20*time.Second)
	if len(r.msgs) != 1 || !strings.Contains(r.msgs[0].text, "for 4m.") || !strings.Contains(r.msgs[0].text, "none") {
		t.Fatalf("got %v", r.msgs)
	}
}

func TestTick_PastMaxDepthFallsToHuman(t *testing.T) {
	r := newRig(t)
	a := jobs.Job{ID: "a", ParentID: "b"}
	b := jobs.Job{ID: "b", ParentID: "a"}
	r.w.state = map[string]*escState{}
	r.gone["a"], r.gone["b"] = true, true
	r.w.escalate(a, map[string]jobs.Job{"a": a, "b": b}, 1, time.Minute, time.Now())
	if len(r.msgs) != 1 || r.msgs[0].to != "" {
		t.Fatalf("got %v", r.msgs)
	}
}

func TestTick_SubMinuteIdleShowsSeconds(t *testing.T) {
	r := newRig(t)
	r.w.IdleAfter = 20 * time.Second
	p := r.job(jobs.KindOther, "")
	r.job(jobs.KindSubagent, p.ID)
	r.tick(20 * time.Second)
	if len(r.msgs) != 1 || !strings.Contains(r.msgs[0].text, "for 20s.") {
		t.Fatalf("got %v", r.msgs)
	}
}

func TestFormatIdle(t *testing.T) {
	for in, want := range map[time.Duration]string{
		20 * time.Second:               "20s",
		4*time.Minute + 50*time.Second: "5m",
		4*time.Minute + 20*time.Second: "4m",
	} {
		if got := formatIdle(in); got != want {
			t.Errorf("%v: got %q want %q", in, got, want)
		}
	}
}

func TestTick_IgnoresWaitingAnswer(t *testing.T) {
	r := newRig(t)
	c := r.job(jobs.KindSubagent, "")
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.reg.Ask(context.Background(), c.ID, "q?")
	}()
	t.Cleanup(func() {
		for !r.reg.Answer(c.ID, "a", false) {
			time.Sleep(time.Millisecond)
		}
		<-done
	})
	for waiting := false; !waiting; time.Sleep(time.Millisecond) {
		for _, j := range r.reg.List() {
			if j.ID == c.ID && j.Status == jobs.StatusWaitingAnswer {
				waiting = true
			}
		}
	}
	r.tick(time.Hour)
	if len(r.msgs) != 0 {
		t.Fatalf("got %v", r.msgs)
	}
}
