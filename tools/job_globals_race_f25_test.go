package tools

// F25: two more of the same B2/F11 shape were found unguarded — a global
// written by a setup/test goroutine, read from a job's own goroutine at
// tool-execution time:
//
//   - globalMCPRunner (mcp.go): written by InitMCP/SetMCPToolRunnerForTests,
//     read by GetMCPToolRunner, which MCP-backed tool calls resolve on their
//     own job goroutine. Same risk profile as the seven B2 globals and the
//     four F11 ones.
//   - SubagentBackgroundAfterSec (subagent.go): read by runWithHandoff's
//     timer on the calling job's own goroutine; production never writes it,
//     only SetSubagentBackgroundAfterSecForTests does (tests shrinking the
//     real 60s wait), so this one's exposure is test-only — same shape as
//     cronRunnerExeOverride.
//
// Both verified manually (see this package's TODO.md F25 entry for the
// exact command/output): reverting either accessor pair back to a plain,
// unguarded var and rerunning `-race` reproduces a DATA RACE on the subtest
// below; with the guard restored, both are race-free.

import "testing"

func TestJobGlobals_F25_ConcurrentSetGet_RaceFree(t *testing.T) {
	oldRunner := GetMCPToolRunner()
	t.Cleanup(func() { SetMCPToolRunnerForTests(oldRunner) })

	oldAfter := SubagentBackgroundAfterSec()
	t.Cleanup(func() { SetSubagentBackgroundAfterSecForTests(oldAfter) })

	cases := []struct {
		name string
		set  func()
		get  func()
	}{
		{
			name: "globalMCPRunner",
			set:  func() { SetMCPToolRunnerForTests(NewMCPToolRunner()) },
			get:  func() { _ = GetMCPToolRunner() },
		},
		{
			name: "subagentBackgroundAfterSec",
			set:  func() { SetSubagentBackgroundAfterSecForTests(oldAfter) },
			get:  func() { _ = SubagentBackgroundAfterSec() },
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			runConcurrentSetGet(t, c.set, c.get)
		})
	}
}
