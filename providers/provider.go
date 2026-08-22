package providers

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/decodo/tyci/api"
	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/internal/agentdefs"
	"github.com/decodo/tyci/internal/instructions"
)

func BuildSystemPrompt() string {
	return buildSystemPrompt(true, "")
}

// BuildSubagentSystemPrompt is the system prompt for a headless child agent
// spawned via the subagent tool. It drops the subagent tool from the listing
// (children cannot spawn further children) and states the subagent contract:
// work autonomously, never ask questions, and end the turn with a single
// self-contained final message that IS the result the parent receives.
func BuildSubagentSystemPrompt() string {
	return buildSystemPrompt(false, `
You are a SUBAGENT spawned by a parent agent to complete ONE task and report back.
- You cannot ask questions — there is no user to reply. Make reasonable assumptions and proceed.
- Do the whole task, then END YOUR TURN with a single self-contained final message that IS your result (the findings/answer/summary the parent needs). The parent sees only your final text, not your tool calls or thinking.
- Do not stop early and do not loop. If you get blocked, state in your final message what you did and exactly what is blocking you.`)
}

// BuildSubagentSystemPromptWithRole returns the standard subagent system
// prompt with a named agent's role appended. The role is the ROLE only — a
// description of the agent's specialization (its markdown definition body) —
// because the harness already supplies the subagent contract, environment
// context (date/cwd/OS), tool descriptions, the project's AGENTS.md and the
// skills list via BuildSubagentSystemPrompt. A definition author therefore
// never has to restate any of that, and it cannot silently drift out of sync
// with the harness as the contract or AGENTS.md evolve — which is exactly
// what happened before this function existed: a named agent's body REPLACED
// the whole prompt, so it lost the contract, AGENTS.md and everything else.
//
// The role is appended AFTER the base prompt is fully assembled, rather than
// threaded through the existing roleNote parameter (which buildSystemPrompt
// splices in at the very top, ahead of the AGENTS.md and skills sections —
// see BuildSubagentSystemPrompt's fixed contract note above). Putting a named
// agent's role there too would bury the environment context and AGENTS.md
// beneath agent-specific prose; a role read LAST, once the model already
// knows the contract and where it is running, reads as a specialization
// layered on stable ground instead. It is joined with the same "\n---\n"
// separator buildSystemPrompt already uses for AGENTS.md and skills, so the
// role shows up as one more clearly delimited section, never silently glued
// onto the prose above it.
func BuildSubagentSystemPromptWithRole(role string) string {
	prompt := BuildSubagentSystemPrompt()
	role = strings.TrimSpace(role)
	if role == "" {
		return prompt
	}
	return prompt + "\n---\nYour role:\n" + role
}

func buildSystemPrompt(includeSubagent bool, roleNote string) string {
	wd, _ := os.Getwd()
	if wd == "" {
		wd = "."
	}

	date := time.Now().Format("2006-01-02")
	osName := runtime.GOOS

	tempDir := "/tmp"
	if osName == "windows" {
		tempDir = "%TEMP%"
	}

	// Posture, tool list and contracts are assembled from three pieces rather
	// than one template: what a top-level agent needs to know and what a child
	// needs are genuinely different sets, and gating with %s placeholders
	// inside one long format string is how they drifted apart before.
	//
	// The whole prompt is deliberately short. An earlier version ran to two
	// long essays — one on delegation, one on batching — that between them
	// restated the same context-window argument three times and carried a
	// fifteen-line lua example. All of it was re-sent on every request, and
	// none of it said the single most unusual thing about this harness: that
	// async jobs NOTIFY you. Density beats completeness here; help(tool) is
	// where completeness lives.
	posture := ""
	contracts := ""
	toolLines := ""
	if includeSubagent {
		posture = `1. Work through MANY agents. subagent(tasks=[...], async=true) spawns parallel children, each with its own context window; you are NOTIFIED when one finishes or blocks on a question — never poll. Delegate anything that means reading more than about three files, and any two independent pieces of work (ONE call, tasks=[...]). Keep for yourself: single-file edits, work needing the exact bytes, work that depends on this conversation.
`
		contracts = `- A child BLOCKED on a question makes no progress and is discarded when it times out: answer(job_id, text) is your NEXT action.
- Parallel children writing shared paths must lock/unlock them — say so in each task's text.
`
		toolLines = `- subagent(task|tasks, agent?, model?, async?): delegate to child agents; tasks=[...] runs them in parallel.
- wait(seconds, job_id?): read a finished job's result, or pause deliberately.
- answer(job_id, text): unblock a child waiting on a question.
- resume(job_id, task): continue a finished async job — it keeps its whole conversation.
- kill_job(job_id): stop a backgrounded shell command.
- agents(name?): named agents usable as subagent(agent="name").
`
	} else {
		posture = `1. Split work you can. You cannot spawn children, but everything below still applies: read narrowly, script your loops, and report a conclusion.
`
		toolLines = `- ask(question): block until the parent answers. Last resort — you stall completely while waiting.
- report_progress(text): post a status note so whoever is watching is not guessing.
- wait(seconds): pause deliberately.
`
	}

	prompt := fmt.Sprintf(`You are tyci, a non-interactive coding agent. There is nobody to ask — decide and act.%s

Context: date %s · working directory %s (do not leave it) · OS %s · temp dir %s.

Posture — four reflexes this environment is built around:
%s2. Write your loops in lua. Any operation over three or more files, or a step that depends on what the last one returned, is ONE lua call: tool(name, args) inside the script, return the conclusion only. What a script reads is thrown away; what YOU read stays in this conversation forever and is re-sent with every later request.
3. Remember in memory. When you work out something a future session would have to work out again — the real test command, a rule the compiler does not enforce, a decision and its reason — write a note. It is loaded into the next session's prompt.
4. Never guess: call help(tool). The list below is one line per tool on purpose. Read help("lua") and help("subagent") before first use.

Contracts — enforced, not advice:
- Your first tool call must be todo(...). Other tools are refused until a plan exists; todo(action="add_batch", items=[...]) is one call for the whole plan.
- write refuses to modify a file you have not read, or that changed since. Read it again and redo the edit against what it says now.
- bash moves to the background after 30s and notifies you when it finishes. Never re-run a backgrounded command.
%s- Hooks may veto or annotate any tool call. A veto is policy, not a bug: change the call, do not retry it verbatim.

Tools — help(tool) for the manual:
- find(pattern, method?): glob file paths, or grep file contents.
- read(path, offset?, limit?, lineNumbers?): read a file.
- write(path, ...): create or overwrite (content+range), or replace exact text (oldString+newString).
- bash(description, command, ...): shell, when no tool fits.
- lua(script, args?): a script that calls other tools; one round trip for a whole loop.
- todo(action, ...): the run's plan. Required first.
- memory(action, name?, content?): project notes that survive the session.
- cron(action, name?, prompt?, schedule?): a prompt that runs later or repeatedly, on its own.
- lock(path) / unlock(path, holder): advisory locks for parallel writes.
%s- help(tool?) · skills(name?): manuals and skills.
- web(method, what): search, lookup or fetch a URL.

A child agent sees ONLY the task text you write — no history, no earlier findings. State what to do, which paths, and exactly what to return, in that order.

Be terse.
`, roleNote, date, wd, osName, tempDir, posture, contracts, toolLines)

	// Standing project context: AGENTS.md plus any notes the agent wrote for
	// itself in an earlier session. An earlier version of this read only
	// ./AGENTS.md, which meant nothing was found when tyci ran from a
	// subdirectory, there was no way to state something once for every
	// project, and an oversized file was pasted into every request unbounded.
	home, _ := os.UserHomeDir()
	prompt += instructions.Load(home, wd)

	// List available skills (names only, not content)
	skillsDir := filepath.Join(os.Getenv("HOME"), ".tyci", "skills")
	if skillNames, err := listSkillNames(skillsDir); err == nil && len(skillNames) > 0 {
		prompt += "\n---\nAvailable skills: " + strings.Join(skillNames, ", ")
		prompt += "\nUse skills(name) to load a skill's full content.\n"
	}

	// List available agents (name + description only) so the model can
	// discover the subagent tool's agent parameter instead of guessing a
	// filename. Only the top-level agent gets this: children cannot spawn
	// further children (see subagentLine above), so listing agents to them
	// would tempt a call to a tool they don't have.
	if includeSubagent {
		if defs := agentdefs.List(wd); len(defs) > 0 {
			prompt += "\n---\nAvailable agents for subagent(agent=\"name\"):\n"
			for _, def := range defs {
				line := "- " + def.Name
				if def.Description != "" {
					line += " — " + def.Description
				}
				prompt += line + "\n"
			}
		}
	}

	return prompt
}

// The canonical message types live in package connector, because providers
// imports connector (and never the other way round). These are type aliases,
// not new types, so every existing consumer — agent, session, the CLI —
// keeps compiling unchanged and the two spellings stay interchangeable.

// ContentBlock represents a single content block within a RichMessage.
type ContentBlock = connector.ContentBlock

// RichMessage is the canonical message type used throughout the agent loop.
type RichMessage = connector.Message

// Request is what a connector.ModelClient sends.
type Request = connector.Request

// Provider is the CATALOG: one named entry answering questions about which
// models it serves and whether it has a credential, plus the factory that
// turns such an entry into something able to send a request.
//
// It deliberately has no Stream. Sending bytes is the job of
// connector.ModelClient, which is the only abstraction package agent ever
// sees, and Client is the only door to it. Before this split the two
// abstractions were glued together, which is how FreeModels() could sit dead
// on the interface for as long as it did — nobody could tell whether it was a
// catalog question or a transport concern.
//
// Minting the client is a METHOD, not a package-level Client(p, model)
// function, because the provider is the only thing that knows how to reach
// its own models (URI, auth, connector kind). A free function would have to
// take the interface and then go looking for the transport behind it, i.e.
// type-assert — and a failed assertion there degrades silently. As a method
// it is checked by the compiler.
type Provider interface {
	Name() string
	IsConfigured() bool
	Models() []string

	// Client returns a ModelClient bound to model on this provider. The name
	// is deliberately NOT validated: `--model provider/anything` has always
	// passed an unlisted name straight through, and the "model not found in
	// provider" error surfaces at request time.
	Client(model string) connector.ModelClient

	// ConfigWarnings reports credential problems that IsConfigured deliberately
	// does NOT report as "not configured" — today, a URI token that looks like
	// "$FOO" but does not resolve through the environment. A single bool
	// cannot carry both "is there a usable credential" (what routing needs:
	// FindModel, catalogResolver, `provider list`'s ✓/✗) and "is something
	// about this credential suspicious" (what a human needs to know to fix a
	// silent 401) — collapsing the second into the first would make
	// IsConfigured's verdict flip out from under callers that only asked the
	// first question. IsConfigured's boolean stays exactly as it is today (see
	// the comment on dynamicProvider.IsConfigured); this is the second,
	// additive channel for the diagnostic that boolean cannot express.
	// Nil means nothing to report.
	ConfigWarnings() []string
}

var DefaultRetryConfig = api.RetryConfig{MaxRetries: 5, BaseBackoff: 4, MaxBackoff: 128}

// listSkillNames returns the names of all skills in the given directory.
func listSkillNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var names []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillPath := filepath.Join(dir, entry.Name(), "SKILL.md")
		if _, err := os.Stat(skillPath); err == nil {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}
