package providers

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// writeAgentDef writes a minimal agent markdown definition file into dir.
func writeAgentDef(t *testing.T, dir, name, description string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	fm := "---\n"
	if description != "" {
		fm += "description: " + description + "\n"
	}
	fm += "---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(fm), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestBuildSystemPrompt_listsAgents: top-level prompt must surface a
// project-defined agent's name and description so the model can discover
// the subagent tool's agent parameter without guessing a filename.
func TestBuildSystemPrompt_listsAgents(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tmp := t.TempDir()
	t.Chdir(tmp)

	writeAgentDef(t, filepath.Join(tmp, ".tyci", "agents"), "reviewer", "Reviews Go diffs for correctness")

	prompt := BuildSystemPrompt()
	if !strings.Contains(prompt, "Available agents for subagent(agent=\"name\"):") {
		t.Fatalf("prompt missing agents header:\n%s", prompt)
	}
	if !strings.Contains(prompt, "reviewer — Reviews Go diffs for correctness") {
		t.Errorf("prompt missing agent name+description line:\n%s", prompt)
	}
}

// TestBuildSubagentSystemPrompt_omitsAgents: children cannot spawn further
// children, so listing agents in a subagent's own prompt would tempt it to
// call a tool it does not have.
func TestBuildSubagentSystemPrompt_omitsAgents(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tmp := t.TempDir()
	t.Chdir(tmp)

	writeAgentDef(t, filepath.Join(tmp, ".tyci", "agents"), "reviewer", "Reviews Go diffs for correctness")

	prompt := BuildSubagentSystemPrompt()
	if strings.Contains(prompt, "Available agents") {
		t.Errorf("subagent prompt should not list agents, but it does:\n%s", prompt)
	}
}

// TestBuildSystemPrompt_noAgents_noHeader: with no agent definitions
// anywhere, the prompt must carry no agents section at all.
func TestBuildSystemPrompt_noAgents_noHeader(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tmp := t.TempDir()
	t.Chdir(tmp)

	prompt := BuildSystemPrompt()
	if strings.Contains(prompt, "Available agents") {
		t.Errorf("expected no agents section when none are defined:\n%s", prompt)
	}
}

// TestBuildSystemPrompt_agentWithoutDescription: an agent with no
// description renders as just its name, with no dangling " — " separator.
func TestBuildSystemPrompt_agentWithoutDescription(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tmp := t.TempDir()
	t.Chdir(tmp)

	writeAgentDef(t, filepath.Join(tmp, ".tyci", "agents"), "explorer", "")

	prompt := BuildSystemPrompt()
	if !strings.Contains(prompt, "- explorer\n") {
		t.Errorf("expected bare agent name line without description:\n%s", prompt)
	}
	if strings.Contains(prompt, "explorer —") {
		t.Errorf("agent without description should not have a dangling separator:\n%s", prompt)
	}
}

// TestBuildSystemPrompt_projectAgentOverridesGlobal: a project-local agent
// definition with the same name as a global one wins, and only one entry
// appears in the prompt.
func TestBuildSystemPrompt_projectAgentOverridesGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	tmp := t.TempDir()
	t.Chdir(tmp)

	writeAgentDef(t, filepath.Join(home, ".tyci", "agents"), "reviewer", "Global description")
	writeAgentDef(t, filepath.Join(tmp, ".tyci", "agents"), "reviewer", "Project description")

	prompt := BuildSystemPrompt()
	if strings.Contains(prompt, "Global description") {
		t.Errorf("project agent should override global, but global description leaked:\n%s", prompt)
	}
	if !strings.Contains(prompt, "reviewer — Project description") {
		t.Errorf("expected project agent's description to win:\n%s", prompt)
	}
	if strings.Count(prompt, "\n- reviewer") != 1 {
		t.Errorf("expected exactly one 'reviewer' entry, got prompt:\n%s", prompt)
	}
}

// TestSystemPromptCarriesTheMultiAgentPosture pins the part that actually
// changes behaviour: countable triggers the model can evaluate against what is
// in front of it, the fact that async jobs NOTIFY rather than need polling, and
// the negative list that stops it delegating trivia.
func TestSystemPromptCarriesTheMultiAgentPosture(t *testing.T) {
	prompt := BuildSystemPrompt()

	for _, want := range []string{
		"MANY agents",
		"three files",
		"tasks=[...]",
		"NOTIFIED",
		"never poll",
		"Keep for yourself",
		"no earlier findings",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("top-level prompt is missing %q", want)
		}
	}
}

// TestSubagentPromptHasNoDelegationPolicy: children cannot spawn children, so
// telling them when to delegate would describe a tool they do not have.
func TestSubagentPromptHasNoDelegationPolicy(t *testing.T) {
	prompt := BuildSubagentSystemPrompt()

	if strings.Contains(prompt, "MANY agents") {
		t.Error("subagent prompt advertises delegation, but children have no subagent tool")
	}
	if strings.Contains(prompt, "tasks=[...]") {
		t.Error("subagent prompt describes a fan-out it cannot perform")
	}
}

// TestPromptStatesTheLuaPosture: a one-line mention of the lua tool does not
// get used, because the model's default is one tool call per step. The trigger
// has to be countable, and the reason has to be the one that matters — context
// spent, not time.
func TestPromptStatesTheLuaPosture(t *testing.T) {
	prompt := BuildSystemPrompt()

	for _, want := range []string{
		"loops in lua",
		"three or more files",
		"thrown away",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the lua posture is missing %q", want)
		}
	}
}

// TestSubagentsAlsoGetTheLuaPosture: a child loops over files as often as its
// parent does, so withholding this would make the delegated half of the work
// the slow half.
func TestSubagentsAlsoGetTheLuaPosture(t *testing.T) {
	if !strings.Contains(BuildSubagentSystemPrompt(), "loops in lua") {
		t.Error("a subagent's prompt has no lua posture")
	}
}

// TestPromptStatesTheEnforcedContracts. These four are not style advice: the
// harness refuses calls over them, and a model that does not know they exist
// reads each refusal as a bug and retries it.
func TestPromptStatesTheEnforcedContracts(t *testing.T) {
	prompt := BuildSystemPrompt()

	for _, want := range []string{
		"first tool call must be todo",
		"write refuses to modify a file you have not read",
		"moves to the background after 30s",
		"Hooks may veto",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the contracts section is missing %q", want)
		}
	}
}

// TestPromptListsEveryToolTheModelHas. Half this environment used to be
// invisible: the inline list named eight tools and the schema carried twenty,
// so the rest existed only for a model that thought to call help().
func TestPromptListsEveryToolTheModelHas(t *testing.T) {
	prompt := BuildSystemPrompt()

	for _, name := range []string{
		"find", "read", "write", "bash", "lua", "todo", "memory", "help", "skills", "web",
		"subagent", "wait", "answer_job", "resume", "kill_job", "agents", "lock", "unlock",
	} {
		if !strings.Contains(prompt, name+"(") {
			t.Errorf("the tool list does not mention %s", name)
		}
	}
}

// TestSubagentPromptListsTheToolsOnlyAChildCanUse: ask_parent and
// report_progress work only inside a job, so they belong in the child's
// list and nowhere else.
func TestSubagentPromptListsTheToolsOnlyAChildCanUse(t *testing.T) {
	child := BuildSubagentSystemPrompt()
	for _, name := range []string{"ask_parent(", "report_progress("} {
		if !strings.Contains(child, name) {
			t.Errorf("a child's prompt does not mention %s", name)
		}
	}

	parent := BuildSystemPrompt()
	for _, name := range []string{"ask_parent(question)", "report_progress(text)"} {
		if strings.Contains(parent, name) {
			t.Errorf("the top-level prompt offers %s, which only works inside a job", name)
		}
	}
}

// noAskContradictionPatterns match the FAMILY of absolute "you cannot ask"
// claims rather than a list of exact phrases, because a check pinned to
// literal strings let "Never ask questions", "You have no way to ask" and
// "do not ask anyone" sail straight through. Case-insensitive: a system
// prompt is not guaranteed to keep any particular capitalization as it
// evolves.
//
// Two patterns, not one, because the two families are shaped differently
// and folding them into a single alternation made it unreadable:
//
//   - the VERB family ("cannot ask", "never ask", "do not ask"). This one
//     requires the ask to be terminated — by an object it is allowed to
//     take, then punctuation — precisely so it does NOT fire on legitimate
//     text like "never ask for permission before reading a file", which a
//     bare `(cannot|never|...)\s+ask` did fire on. A guard that cries wolf
//     on innocent prose gets deleted by the first person it annoys, so the
//     narrower pattern is the more durable one.
//   - the NO-COUNTERPARTY family ("nobody to ask", "no user to reply").
//     This one is not optional: it is the exact shape of both strings item
//     41 deleted, and it is how the bug most plausibly comes back — by
//     someone collapsing buildSystemPrompt's gated `header` (provider.go)
//     back into one shared line, which would put "There is nobody to ask"
//     into the child prompt again. A verb-only pattern is silent on that.
var noAskContradictionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(cannot|can not|never|do not|don't|no way to)\s+ask(\s+(questions?|anything|anyone|any\s+one|the\s+user|your\s+parent))?\s*[.,;:—-]`),
	regexp.MustCompile(`(?i)no(body|\s+one|\s+user|\s+parent)\s+to\s+(ask|reply|answer)`),
}

// TestSubagentPromptDoesNotContradictItsOwnAskParentTool guards against item
// 41 regressing: the child prompt hands out a real ask_parent tool, so it
// must never simultaneously claim in absolute terms that it cannot ask. That
// contradiction shipped for a while ("You cannot ask questions — there is no
// user to reply" right above a working ask_parent tool) and made children's
// use of ask_parent unpredictable, since they had to disbelieve their own
// system prompt to use the tool they were given.
func TestSubagentPromptDoesNotContradictItsOwnAskParentTool(t *testing.T) {
	// Isolate from the developer's real ~/.tyci/AGENTS.md (see
	// writeAgentDef's callers above for the same pattern) — otherwise
	// whatever that file happens to say becomes part of the string this
	// test asserts against, which has nothing to do with item 41.
	t.Setenv("HOME", t.TempDir())

	prompt := BuildSubagentSystemPrompt()

	if !strings.Contains(prompt, "ask_parent(") {
		t.Fatalf("expected the child prompt to expose ask_parent(), it did not:\n%s", prompt)
	}
	for _, pat := range noAskContradictionPatterns {
		if m := pat.FindString(prompt); m != "" {
			t.Errorf("child prompt contains an absolute claim (%q) that it cannot ask, alongside a working ask_parent tool:\n%s", m, prompt)
		}
	}
}

// TestNoAskContradictionPatterns_CatchTheRealFamilyWithoutFalseTripping is a
// guard for the guard above. The check it protects is only worth having if it
// (a) still fires on the two strings item 41 actually deleted and (b) stays
// quiet on legitimate prompt text — an earlier single-regexp version failed
// BOTH: it missed "There is nobody to ask" and "no user to reply" (the exact
// strings that shipped the contradiction) while firing on "never ask for
// permission before reading a file". Without this test that regression is
// invisible, because a guard that matches nothing passes just as quietly as
// a guard that works.
func TestNoAskContradictionPatterns_CatchTheRealFamilyWithoutFalseTripping(t *testing.T) {
	matchesAny := func(s string) bool {
		for _, pat := range noAskContradictionPatterns {
			if pat.MatchString(s) {
				return true
			}
		}
		return false
	}

	// Must fire. The first two are verbatim from the prompt item 41 fixed.
	for _, s := range []string{
		"- You cannot ask questions — there is no user to reply. Make reasonable assumptions and proceed.",
		"You are tyci, a non-interactive coding agent. There is nobody to ask — decide and act.",
		"Never ask questions.",
		"You have no way to ask.",
		"do not ask anyone.",
		"don't ask, just decide.",
		"There is no one to ask.",
		"Nobody to reply, so proceed.",
	} {
		if !matchesAny(s) {
			t.Errorf("guard missed an absolute no-ask claim: %q", s)
		}
	}

	// Must stay quiet. These are things a prompt may legitimately say.
	for _, s := range []string{
		"never ask for permission before reading a file",
		"do not ask for confirmation; just proceed",
		"ask_parent is a real tool and a genuine last resort",
		"If you are truly stuck with no way to get an answer — or you already asked and got none — say so.",
		"never for a preference or style question",
	} {
		if matchesAny(s) {
			t.Errorf("guard false-tripped on legitimate text: %q", s)
		}
	}
}

// TestPromptStaysShort. The prompt is re-sent with every request, and an
// earlier version had grown to two essays restating the same argument three
// times plus a fifteen-line code example that already lives in help("lua").
// Density is the point; this is the guard that keeps it.
func TestPromptStaysShort(t *testing.T) {
	const budget = 5000
	if got := len(BuildSystemPrompt()); got > budget {
		t.Errorf("system prompt is %d bytes, budget is %d — put the detail in help(tool) instead", got, budget)
	}
}
