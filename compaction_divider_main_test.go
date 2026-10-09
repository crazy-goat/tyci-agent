package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/session"
)

// dividerCapture is captureDisplay that also records the order of blocks and
// compaction dividers.
type dividerCapture struct {
	*captureDisplay
	order []string
}

func (d *dividerCapture) ToolBlock(msg string) {
	d.order = append(d.order, "block")
	d.captureDisplay.ToolBlock(msg)
}

func (d *dividerCapture) Compaction(meta session.CompactMeta) {
	d.order = append(d.order, "divider:"+meta.Kind)
}

func TestReplaySessionToDisplayShowsDividerInOrder(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/divider.jsonl"
	s, err := session.Open(path, dir, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMessage("user", []session.ContentBlock{{Type: "text", Text: "before"}}, nil); err != nil {
		t.Fatal(err)
	}
	meta := session.CompactMeta{Kind: session.CompactKindAuto, TokensBefore: 1000, At: time.Now()}
	if err := s.WriteCompaction("summary", "", nil, 0, meta); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMessage("user", []session.ContentBlock{{Type: "text", Text: "after"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	d := &dividerCapture{captureDisplay: newCapture()}
	replaySessionToDisplay(d, path)

	var dividers, blocksBeforeDivider int
	for _, o := range d.order {
		if strings.HasPrefix(o, "divider:") {
			if o != "divider:"+session.CompactKindAuto {
				t.Fatalf("divider kind = %q", o)
			}
			dividers++
			continue
		}
		if dividers == 0 {
			blocksBeforeDivider++
		}
	}
	if dividers != 1 {
		t.Fatalf("dividers = %d, want 1 (order %v)", dividers, d.order)
	}
	if blocksBeforeDivider == 0 {
		t.Fatalf("no block before the divider (order %v)", d.order)
	}
}

func TestCompactAndShowDividerOnlyOnSuccess(t *testing.T) {
	meta := session.CompactMeta{Kind: session.CompactKindCommand}
	var ok, failed []session.CompactMeta

	okCompact := func(summary, focus string, m session.CompactMeta) (string, error) { return "/tmp/d.md", nil }
	failCompact := func(summary, focus string, m session.CompactMeta) (string, error) { return "", errors.New("boom") }

	recOK := &metaRecorder{onMeta: func(m session.CompactMeta) { ok = append(ok, m) }}
	if _, err := compactAndShow(recOK, okCompact, "sum", "", meta); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	recFail := &metaRecorder{onMeta: func(m session.CompactMeta) { failed = append(failed, m) }}
	if _, err := compactAndShow(recFail, failCompact, "sum", "", meta); err == nil {
		t.Fatal("expected error")
	}
	if len(ok) != 1 || ok[0].Kind != session.CompactKindCommand {
		t.Fatalf("success dividers = %+v, want one /compact divider", ok)
	}
	if len(failed) != 0 {
		t.Fatalf("failed compaction showed %d dividers", len(failed))
	}
}

type metaRecorder struct {
	onMeta func(session.CompactMeta)
}

func (m *metaRecorder) Compaction(meta session.CompactMeta) { m.onMeta(meta) }
