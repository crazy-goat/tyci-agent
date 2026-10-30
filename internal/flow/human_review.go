package flow

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// humanReviewAnswer accepts only the three dedicated human outcomes. Optional
// text on a fix or decline goes to the next worker as a note. Comment storage,
// composing fix prompts and the review UI are separate from this state kind.
func humanReviewAnswer(s State, answer string) (key, note, next string, err error) {
	key, note, _ = strings.Cut(strings.TrimSpace(answer), " ")
	note = strings.TrimSpace(note)
	switch key {
	case "accept":
		if note != "" {
			key = ""
		}
	case "fix", "decline":
	default:
		key = ""
	}
	next = s.On[key]
	if key == "" || next == "" || next == FailedTarget {
		return "", "", "", fmt.Errorf("flow: unknown human review answer %q, allowed: accept, fix, decline", answer)
	}
	return key, MaskSecrets(note), next, nil
}

// resumeHumanReview records the decision in state.json before executing its
// target. Unlike an ordinary ask answer it keeps every visit counter, so a fix
// consumes another code visit rather than granting a fresh budget.
func (r *Runner) resumeHumanReview(ctx context.Context, st *RunState, s State, answer string) error {
	key, note, next, err := humanReviewAnswer(s, answer)
	if err != nil {
		return err
	}
	if err := r.recordApproval(st, answer); err != nil {
		return err
	}
	if note != "" {
		st.Note = note
	}
	now := time.Now()
	step := Step{
		Seq: len(st.History) + 1, State: st.Current, Kind: "human_review",
		Key: key, Note: note, To: next, StartedAt: now, EndedAt: now,
	}
	return r.moveOn(ctx, st, step, next, false)
}
