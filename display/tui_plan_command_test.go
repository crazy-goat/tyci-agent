package display

import "testing"

// TestPlanWhileBusyGoesToTheCommandChannel: /plan starts a planner job, not a
// run. While a turn is in flight it is routed to the main loop, not sent to
// the model as a prompt.
func TestPlanWhileBusyGoesToTheCommandChannel(t *testing.T) {
	for _, typed := range []string{"/plan", "/plan write the auth plan"} {
		m := busyModel(t, typed)
		handled, next := m.handleLocalSlashCommand()
		if !handled {
			t.Fatalf("%q fell through to submit(), so the model was asked to interpret it", typed)
		}
		select {
		case cmd := <-next.(TuiModel).commands:
			if cmd != typed {
				t.Fatalf("command channel got %q, want %q", cmd, typed)
			}
		default:
			t.Fatalf("%q was swallowed: nothing reached the command channel", typed)
		}
	}
}

// TestPlanIsAListedBuiltinCommand: "/plan" is in the "/" list, and a workflow
// cannot take its name.
func TestPlanIsAListedBuiltinCommand(t *testing.T) {
	if !IsReservedWorkflowName("plan") {
		t.Fatal("plan is not a reserved builtin command name")
	}
	m := TuiModel{}
	for _, row := range m.slashCommandRows() {
		if row.name == "plan" && row.label != "" {
			return
		}
	}
	t.Fatal("/plan is not in the slash command list")
}
