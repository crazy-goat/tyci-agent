package tools

import (
	"context"
)

// fakeJobAsker is a minimal JobAsker whose Ask always returns a fixed
// canned answer/fromUser/ok triple, for exercising AskTool.Run's handling
// of the provenance flag without a real jobs.Registry.
type fakeJobAsker struct {
	answer   string
	fromUser bool
	ok       bool
}

func (f fakeJobAsker) Ask(ctx context.Context, id, question string) (string, bool, bool) {
	return f.answer, f.fromUser, f.ok
}

// fakeJobAnswerer records the fromUser flag it was called with, so
// AnswerTool.Run's contract (it always represents the model, never a
// person, so it must always pass fromUser=false) can be checked directly.
type fakeJobAnswerer struct {
	gotID       string
	gotText     string
	gotFromUser bool
	ret         bool
}

func (f *fakeJobAnswerer) Answer(id, text string, fromUser bool) bool {
	f.gotID, f.gotText, f.gotFromUser = id, text, fromUser
	return f.ret
}
