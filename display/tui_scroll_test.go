package display

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTuiModel_SubmitCreatesUserBlockAndResetsScroll(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.width = 40
	m.height = 10
	m.scrollLine = 5
	m.input.SetValue("next prompt")

	updated := m.submit().(TuiModel)

	if updated.scrollLine != 0 {
		t.Fatalf("expected submit to reset scroll to bottom, got %d", updated.scrollLine)
	}
	if len(updated.blocks) != 1 {
		t.Fatalf("expected one block, got %d", len(updated.blocks))
	}
	if updated.blocks[0].kind != "user" {
		t.Fatalf("expected user block, got %q", updated.blocks[0].kind)
	}
}

func TestTuiModel_AssistantTextDoesNotAppendToSubmittedUserPrompt(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.width = 40
	m.height = 10
	m.input.SetValue("prompt")
	m = m.submit().(TuiModel)

	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "assistant answer"})

	if len(m.blocks) != 2 {
		t.Fatalf("expected user and assistant blocks, got %d", len(m.blocks))
	}
	if m.blocks[0].kind != "user" || !strings.Contains(m.blocks[0].content, "prompt") {
		t.Fatalf("unexpected first block: kind=%q content=%q", m.blocks[0].kind, m.blocks[0].content)
	}
	if m.blocks[1].kind != "text" || m.blocks[1].content != "assistant answer" {
		t.Fatalf("unexpected assistant block: kind=%q content=%q", m.blocks[1].kind, m.blocks[1].content)
	}
}

func TestTuiModel_KeyEndRestoresAutoScrollAfterPrompt(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil)
	m.width = 30
	m.height = 8
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: strings.Repeat("old content ", 40)})
	m.handleBlockMsg(tuiMsgBlock{kind: "done"})
	m.scrollLine = 10
	m.input.SetValue("prompt")
	m = m.submit().(TuiModel)

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = model.(TuiModel)
	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: strings.Repeat("new content ", 40)})

	if m.scrollLine != 0 {
		t.Fatalf("expected streaming to stay at bottom after End, got scrollLine=%d", m.scrollLine)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "new content") {
		t.Fatalf("expected view to include new streamed content, got:\n%s", view)
	}
}
