package display

// What a tool call looks like in the transcript. "lua(...)" and "memory(...)"
// made every call of those tools identical on screen — you could see that
// something happened and not what.

import (
	"testing"
)

func label(toolName, argsJSON string) string {
	return formatToolCall(toolName, argsJSON)
}

func TestLuaLabelUsesTheDescription(t *testing.T) {
	got := label("lua", `{"description":"rename oldName across the Go files","script":"local x = 1"}`)
	if got != "lua(rename oldName across the Go files)" {
		t.Fatalf("got %q", got)
	}
}

// TestLuaLabelFallsBackToTheFirstCodeLine: a description is the model's job and
// it will sometimes omit it. The first real line of the script says more than
// "..." — and skipping comments matters, or a script opening with a comment
// gets labelled with the comment marker instead of what it does.
func TestLuaLabelFallsBackToTheFirstCodeLine(t *testing.T) {
	got := label("lua", `{"script":"-- find every handler\n\nlocal hits = tool(\"find\", {})"}`)
	if got != `lua(local hits = tool("find", {}))` {
		t.Fatalf("got %q", got)
	}
}

func TestLuaLabelWithNothingUsableStillSaysSomething(t *testing.T) {
	if got := label("lua", `{"script":"-- only a comment"}`); got != "lua(script)" {
		t.Fatalf("got %q", got)
	}
}

// TestMemoryLabelShowsActionAndNote: no new parameter was needed here, the
// arguments already said everything — they were simply not shown.
func TestMemoryLabelShowsActionAndNote(t *testing.T) {
	cases := map[string]string{
		`{"action":"write","name":"test-command","content":"..."}`: "memory(write, test-command)",
		`{"action":"read","name":"layering"}`:                      "memory(read, layering)",
		`{"action":"list"}`:                                        "memory(list)",
		`{}`:                                                       "memory(list)",
		`{"action":"delete","name":"stale-note"}`:                  "memory(delete, stale-note)",
	}
	for args, want := range cases {
		if got := label("memory", args); got != want {
			t.Errorf("%s -> %q, want %q", args, got, want)
		}
	}
}

func TestHelpLabelNamesTheTool(t *testing.T) {
	if got := label("help", `{"tool":"jobs"}`); got != "help(jobs)" {
		t.Errorf("got %q", got)
	}
	if got := label("help", `{}`); got != "help(list)" {
		t.Errorf("got %q", got)
	}
}

// TestLabelsStillWorkForTheOlderTools guards the cases that already worked, so
// adding branches to the same switch cannot quietly break them.
func TestLabelsStillWorkForTheOlderTools(t *testing.T) {
	cases := []struct{ tool, args, want string }{
		{"read", `{"path":"main.go"}`, "read(main.go)"},
		{"write", `{"path":"a/b.go","content":"x"}`, "write(a/b.go)"},
		{"bash", `{"command":"ls","description":"list files"}`, "bash(list files)"},
		{"find", `{"method":"grep","pattern":"TODO"}`, "find(grep, TODO)"},
		{"skills", ``, "skills(list)"},
	}
	for _, c := range cases {
		if got := label(c.tool, c.args); got != c.want {
			t.Errorf("%s(%s) -> %q, want %q", c.tool, c.args, got, c.want)
		}
	}
}
