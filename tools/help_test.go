package tools

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

func runHelp(t *testing.T, tool string) ToolResult {
	t.Helper()
	input := map[string]any{}
	if tool != "" {
		input["tool"] = tool
	}
	return (&HelpTool{}).Run(context.Background(), input)
}

func TestHelpIndexListsEveryTool(t *testing.T) {
	res := runHelp(t, "")
	if !res.Success {
		t.Fatalf("%s", res.Error)
	}
	// Every registered tool must be findable, or help sends the model looking
	// for documentation that does not exist.
	for _, name := range toolNames() {
		if !strings.Contains(res.Content, name) {
			t.Errorf("%q is registered but missing from the index", name)
		}
	}
	if !strings.Contains(res.Content, "help(tool=") {
		t.Error("the index should say how to read one article")
	}
}

// TestHelpHasAnArticleForTheHighLeverageTools: these are the two whose
// one-line description most understates what they do, which is the reason this
// tool exists at all.
func TestHelpHasAnArticleForTheHighLeverageTools(t *testing.T) {
	for _, name := range []string{"lua", "subagent"} {
		res := runHelp(t, name)
		if !res.Success {
			t.Fatalf("%s: %s", name, res.Error)
		}
		if len(res.Content) < 800 {
			t.Errorf("help(%q) is only %d bytes; the point is the long version", name, len(res.Content))
		}
		if !strings.Contains(res.Content, "## Parameters") {
			t.Errorf("help(%q) has no parameter list", name)
		}
	}
}

// TestHelpArticlesContainAWorkedExample: rules alone do not change behaviour;
// an example does. Checked mechanically so an article cannot be trimmed down
// to prose without the test noticing.
func TestHelpArticlesContainAWorkedExample(t *testing.T) {
	for _, name := range []string{"lua", "subagent", "write", "bash", "find", "memory", "todo"} {
		article, ok := toolHelp[name]
		if !ok {
			t.Errorf("no article for %q", name)
			continue
		}
		if !strings.Contains(article, "  ") {
			t.Errorf("article for %q has no indented example", name)
		}
	}
}

// TestHelpFallsBackToTheSchema: a tool without an article — an MCP tool, a
// .lua tool, anything added later — must still be answerable, or help teaches
// the model that some tools simply have no documentation.
func TestHelpFallsBackToTheSchema(t *testing.T) {
	res := runHelp(t, "web")
	if !res.Success {
		t.Fatalf("%s", res.Error)
	}
	if _, hasArticle := toolHelp["web"]; hasArticle {
		t.Skip("web now has an article; pick another tool for this test")
	}
	if !strings.Contains(res.Content, "## Parameters") {
		t.Error("the fallback should still list the parameters")
	}
	if !strings.Contains(res.Content, "No long-form article") {
		t.Error("the fallback should say that is all there is, not pretend otherwise")
	}
}

func TestHelpOnAnUnknownToolPointsAtTheIndex(t *testing.T) {
	res := runHelp(t, "nosuchtool")
	if res.Success {
		t.Fatal("expected a failure")
	}
	if !strings.Contains(res.Error, "help()") {
		t.Errorf("the error should say how to find the real names: %q", res.Error)
	}
}

// TestHelpArticlesNameOnlyRealTools guards against an article written for a
// tool that was later renamed or removed. Checked against the schema rather
// than the registry, because "subagent" is wired in from main rather than
// registered here.
func TestHelpArticlesNameOnlyRealTools(t *testing.T) {
	known := map[string]bool{}
	for _, entry := range GetAllToolsSchema() {
		if fn, ok := entry["function"].(map[string]any); ok {
			if name, _ := fn["name"].(string); name != "" {
				known[name] = true
			}
		}
	}
	// A few articles describe a topic spanning several tools rather than one
	// tool; they are listed here so a typo in a tool name still fails.
	topics := map[string]bool{"jobs": true}
	for name := range toolHelp {
		if !known[name] && !topics[name] {
			t.Errorf("article for %q, which no schema entry names", name)
		}
	}
}

// TestHelpParametersMatchTheSchema is the drift check: an article is
// hand-written prose and will rot, so the parameter list beside it always
// comes from the schema the model is actually being sent.
func TestHelpParametersMatchTheSchema(t *testing.T) {
	res := runHelp(t, "write")
	if !res.Success {
		t.Fatalf("%s", res.Error)
	}
	for _, param := range []string{"oldString", "newString", "occurrence", "dryRun", "range"} {
		if !strings.Contains(res.Content, param) {
			t.Errorf("help(write) does not mention the %q parameter", param)
		}
	}
}

// TestHelpJobsExplainsTheWholeLifecycle. The orchestration story is the part
// of this environment a model has no prior for, and it used to be told in
// fragments: ask, answer, wait, resume and lock each described their own step
// and nothing described how they fit together.
func TestHelpJobsExplainsTheWholeLifecycle(t *testing.T) {
	res := runHelp(t, "jobs")
	if !res.Success {
		t.Fatalf("%s", res.Error)
	}
	for _, want := range []string{
		"tasks = [",
		"notified",
		"do NOT poll",
		"answer_job(job_id",
		"discarded",
		"resume(job_id",
		"lock(path",
		"kill_job(job_id",
		"report_progress",
	} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("help(\"jobs\") is missing %q", want)
		}
	}
	// It is a topic, not a tool: nothing to call, and saying so avoids the
	// model trying jobs(...).
	if !strings.Contains(res.Content, "not a tool") {
		t.Error("the article should say there is nothing to call by this name")
	}
}

// TestHelpRelaysRatherThanAnswersUnconditionally guards item 29's reword of
// two of the five model-facing strings that used to tell the model to
// answer(...) as if it were the only channel — one in help_articles.go's
// "subagent" article (what was line 104 before the rename), one in its
// "jobs" article (what was line 255). Both must now tell the model to
// relay to the user (or genuinely-known info) unless it truly knows the
// answer, using the renamed answer_job tool, and never invent one.
func TestHelpRelaysRatherThanAnswersUnconditionally(t *testing.T) {
	subagent := runHelp(t, "subagent")
	if !subagent.Success {
		t.Fatalf("%s", subagent.Error)
	}
	if !strings.Contains(subagent.Content, "relay it") {
		t.Errorf("help(\"subagent\") should tell the model to relay the answer, not invent one:\n%s", subagent.Content)
	}
	if !strings.Contains(subagent.Content, "unless you truly know the answer") {
		t.Errorf("help(\"subagent\") should reserve answering directly for when the model genuinely knows:\n%s", subagent.Content)
	}
	if !strings.Contains(subagent.Content, "invent one standing in for a human who hasn't replied") {
		t.Errorf("help(\"subagent\") should say never to invent an answer standing in for the user:\n%s", subagent.Content)
	}

	jobsArticle := runHelp(t, "jobs")
	if !jobsArticle.Success {
		t.Fatalf("%s", jobsArticle.Error)
	}
	if !strings.Contains(jobsArticle.Content, "relay it") {
		t.Errorf("help(\"jobs\") should tell the model to relay the answer, not invent one:\n%s", jobsArticle.Content)
	}
	if !strings.Contains(jobsArticle.Content, "you truly know the answer") {
		t.Errorf("help(\"jobs\") should reserve answering directly for when the model genuinely knows:\n%s", jobsArticle.Content)
	}
	if !strings.Contains(jobsArticle.Content, "never one") || !strings.Contains(jobsArticle.Content, "stand in for a human who hasn't replied") {
		t.Errorf("help(\"jobs\") should say never to invent an answer standing in for the user:\n%s", jobsArticle.Content)
	}
}

// TestHelpIndexPointsAtTheJobsArticle: an article nobody is sent to is an
// article nobody reads.
func TestHelpIndexPointsAtTheJobsArticle(t *testing.T) {
	res := runHelp(t, "")
	if !strings.Contains(res.Content, `help("jobs")`) {
		t.Fatalf("the index does not point at the jobs article:\n%s", res.Content)
	}
}

// TestNothingTheModelReadsTeachesPolling. This harness pushes notices; a model
// told to poll in one place and notified in another will poll, and polling
// costs a round trip per check.
//
// Both surfaces are checked, because checking only the descriptions is what let
// the real case through: the schema said nothing about polling while the bash
// handoff RESULT said "you can poll it with wait(job_id=...)" — and a result is
// read at the exact moment the model is deciding what to do next, so it is the
// more persuasive of the two.
func TestNothingTheModelReadsTeachesPolling(t *testing.T) {
	bad := []string{"poll it", "poll the", "poll for", "keep polling"}

	for _, entry := range GetAllToolsSchema() {
		fn, ok := entry["function"].(map[string]any)
		if !ok {
			continue
		}
		name, _ := fn["name"].(string)
		desc, _ := fn["description"].(string)
		params, _ := fn["parameters"].(map[string]any)
		text := strings.ToLower(desc + " " + fmt.Sprint(params))
		for _, phrase := range bad {
			if strings.Contains(text, phrase) {
				t.Errorf("%s's schema says %q — this harness notifies, it is not polled", name, phrase)
			}
		}
	}

	// The result strings a model acts on. Read from source rather than
	// reproduced here, so the check cannot drift from what is shipped.
	for _, file := range []string{"bash.go", "wait.go", "subagent.go", "resume.go"} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			lower := strings.ToLower(line)
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue // comments explain the rule; they are not sent to the model
			}
			for _, phrase := range bad {
				if strings.Contains(lower, phrase) {
					t.Errorf("%s tells the model to %q:\n  %s", file, phrase, strings.TrimSpace(line))
				}
			}
		}
	}
}

// TestBackgroundHandoffForbidsAnImmediateWait: the handoff is read at the exact
// moment the model picks its next action, so offering wait there is an
// invitation. It has to name the alternative instead.
func TestBackgroundHandoffForbidsAnImmediateWait(t *testing.T) {
	data, err := os.ReadFile("bash.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	for _, want := range []string{
		"Do NOT run this command again",
		"Do NOT call wait for it either",
		"a notice reaches you",
		"kill_job(job_id=",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the background handoff no longer says %q", want)
		}
	}
}
