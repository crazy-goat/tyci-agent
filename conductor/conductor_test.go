package conductor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/session"
	"github.com/decodo/tyci/stream"
)

// ---------------------------------------------------------------------------
// Local doubles.
//
// What is left here is deliberately local: a Sink and a ToolRunner, both of
// them interfaces this package declares for its own consumers. The scripted
// connector.ModelClient that used to sit alongside them is gone — stage 7 of
// the refactor moved it into connector/connectortest, where every package
// that needs a fake model can reach it. The record/replay pair from that
// stage is still to come.
// ---------------------------------------------------------------------------

// recorder is an agent.Sink that keeps what it was told, and nothing else.
// It writes to no terminal, holds no file descriptor and needs no TTY.
type recorder struct {
	text      strings.Builder
	thinking  strings.Builder
	toolStart []string
	toolEnd   []string
	blocks    []string
	errs      []error
	ends      int
	totals    []stream.Usage
}

func (r *recorder) Request(string)                     {}
func (r *recorder) Thinking(t string)                  { r.thinking.WriteString(t) }
func (r *recorder) Text(t string)                      { r.text.WriteString(t) }
func (r *recorder) ToolCallStart(name string)          { r.toolStart = append(r.toolStart, name) }
func (r *recorder) ToolCallDelta(string)               {}
func (r *recorder) ToolCallEnd(n, res string)          { r.toolEnd = append(r.toolEnd, n+"="+res) }
func (r *recorder) ToolFinish()                        {}
func (r *recorder) ToolBlock(m string)                 { r.blocks = append(r.blocks, m) }
func (r *recorder) Summary(stream.Usage, stream.Stats) {}
func (r *recorder) Total(u stream.Usage)               { r.totals = append(r.totals, u) }
func (r *recorder) Error(err error)                    { r.errs = append(r.errs, err) }
func (r *recorder) End()                               { r.ends++ }

// scriptedTools answers every tool call from a table, and records the calls.
type scriptedTools struct {
	answers map[string]string
	mu      sync.Mutex
	calls   []string
}

func (s *scriptedTools) Run(_ context.Context, name string, args map[string]any) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, _ := json.Marshal(args)
	s.calls = append(s.calls, name+string(raw))
	if out, ok := s.answers[name]; ok {
		return out, nil
	}
	return "", fmt.Errorf("unknown tool: %s", name)
}

// mapResolver is a ModelResolver backed by a table instead of a catalog —
// the whole point of the interface being declared on the consumer side.
type mapResolver struct {
	models map[string]connector.ModelClient
}

var errNoSuchModel = errors.New("no such model")

func (m mapResolver) Resolve(spec string) (connector.ModelClient, error) {
	mc, ok := m.models[spec]
	if !ok {
		return nil, fmt.Errorf("%w: %s", errNoSuchModel, spec)
	}
	return mc, nil
}

// ---------------------------------------------------------------------------
// The smoke test this whole stage exists for.
// ---------------------------------------------------------------------------

// TestConductor_HeadlessConversation drives a complete conversation —
// user prompt, model asks for a tool, tool runs, model answers — with no UI
// whatsoever: no TUI, no terminal, no readline, no os.Stdout. The only
// collaborators are a scripted connector.ModelClient, a slice-backed Sink and
// a map-backed ToolRunner.
//
// This is the test that could not have been written before this stage: the
// loop it exercises used to live inside runTUI / runInteractive / runPrompt,
// each of which needs a display, a terminal and a cobra command to reach.
func TestConductor_HeadlessConversation(t *testing.T) {
	client := &connectortest.Fake{
		ProviderName: "fakeprov",
		ModelName:    "fakemodel",
		Turns: [][]stream.Event{
			{
				stream.ToolCallStart{ID: "call-1", Name: "echo"},
				stream.ToolCall{ID: "call-1", Name: "echo", Arguments: `{"text":"hello"}`},
				stream.Finish{Reason: "tool_calls", Usage: stream.Usage{Input: 10, Output: 5}},
			},
			{
				stream.TextDelta{Text: "the tool said hello"},
				stream.Finish{Reason: "stop", Usage: stream.Usage{Input: 20, Output: 7}},
			},
		},
	}
	sink := &recorder{}
	toolRunner := &scriptedTools{answers: map[string]string{"echo": "hello"}}

	c := New(Options{
		Client: client,
		Sink:   sink,
		Config: agent.Config{
			System: "be brief",
			Tools:  toolRunner,
			Schema: json.RawMessage(`[]`),
		},
	})

	usage, err := c.Submit(context.Background(), "say hello with the echo tool")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// The model was asked twice: once for the tool call, once with the
	// tool result fed back in. That round trip is the conversation.
	if client.Calls() != 2 {
		t.Fatalf("model called %d times, want 2", client.Calls())
	}
	if got := len(client.Requests()[0].Messages); got != 1 {
		t.Errorf("first request carried %d messages, want 1 (the user prompt)", got)
	}
	if len(client.Requests()[1].Messages) <= len(client.Requests()[0].Messages) {
		t.Errorf("second request did not grow: %d then %d messages",
			len(client.Requests()[0].Messages), len(client.Requests()[1].Messages))
	}
	if client.Requests()[0].System != "be brief" {
		t.Errorf("system prompt = %q, want %q", client.Requests()[0].System, "be brief")
	}
	if client.Requests()[0].Model != "fakemodel" {
		t.Errorf("request model = %q, want fakemodel", client.Requests()[0].Model)
	}

	// The tool actually ran, with the arguments the model streamed.
	if len(toolRunner.calls) != 1 || toolRunner.calls[0] != `echo{"text":"hello"}` {
		t.Errorf("tool calls = %v, want one echo{\"text\":\"hello\"}", toolRunner.calls)
	}

	// The answer reached the sink.
	if sink.text.String() != "the tool said hello" {
		t.Errorf("sink text = %q", sink.text.String())
	}
	if len(sink.errs) != 0 {
		t.Errorf("sink saw errors: %v", sink.errs)
	}

	// Usage from both turns is summed, and the conductor keeps the total.
	want := stream.Usage{Input: 30, Output: 12}
	if usage != want {
		t.Errorf("turn usage = %+v, want %+v", usage, want)
	}
	if c.Usage() != want {
		t.Errorf("accumulated usage = %+v, want %+v", c.Usage(), want)
	}

	// The conversation the conductor now owns: user, assistant(tool call),
	// tool result, assistant(text).
	roles := messageRoles(c.Messages())
	wantRoles := []string{"user", "assistant", "toolResult", "assistant"}
	if strings.Join(roles, ",") != strings.Join(wantRoles, ",") {
		t.Errorf("conversation roles = %v, want %v", roles, wantRoles)
	}
}

func messageRoles(msgs []connector.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Role
	}
	return out
}

// ---------------------------------------------------------------------------
// Submit / usage
// ---------------------------------------------------------------------------

// TestConductor_SubmitAccumulatesUsageAcrossTurns verifies that Submit returns
// the usage of the turn it just ran while Usage() keeps the running total —
// the split every frontend needs (the TUI shows the turn, session_end records
// the total).
func TestConductor_SubmitAccumulatesUsageAcrossTurns(t *testing.T) {
	client := &connectortest.Fake{
		ProviderName: "p", ModelName: "m",
		Turns: [][]stream.Event{
			{stream.TextDelta{Text: "one"}, stream.Finish{Usage: stream.Usage{Input: 3, Output: 1}}},
			{stream.TextDelta{Text: "two"}, stream.Finish{Usage: stream.Usage{Input: 4, Output: 2}}},
		},
	}
	c := New(Options{Client: client, Sink: &recorder{}, Config: agent.Config{}})

	first, err := c.Submit(context.Background(), "a")
	if err != nil {
		t.Fatalf("first Submit: %v", err)
	}
	if first != (stream.Usage{Input: 3, Output: 1}) {
		t.Errorf("first turn usage = %+v", first)
	}
	second, err := c.Submit(context.Background(), "b")
	if err != nil {
		t.Fatalf("second Submit: %v", err)
	}
	if second != (stream.Usage{Input: 4, Output: 2}) {
		t.Errorf("second turn usage = %+v", second)
	}
	if c.Usage() != (stream.Usage{Input: 7, Output: 3}) {
		t.Errorf("total usage = %+v, want {7 3}", c.Usage())
	}
}

// TestConductor_SubmitKeepsHistoryAcrossTurns verifies the property that made
// the conversation worth owning in one place: the second prompt is sent with
// the first turn still in the request.
func TestConductor_SubmitKeepsHistoryAcrossTurns(t *testing.T) {
	client := &connectortest.Fake{
		ProviderName: "p", ModelName: "m",
		Turns: [][]stream.Event{
			{stream.TextDelta{Text: "one"}, stream.Finish{}},
			{stream.TextDelta{Text: "two"}, stream.Finish{}},
		},
	}
	c := New(Options{Client: client, Sink: &recorder{}, Config: agent.Config{}})

	if _, err := c.Submit(context.Background(), "a"); err != nil {
		t.Fatalf("Submit a: %v", err)
	}
	if _, err := c.Submit(context.Background(), "b"); err != nil {
		t.Fatalf("Submit b: %v", err)
	}
	if got := len(client.Requests()[1].Messages); got != 3 {
		t.Errorf("second request had %d messages, want 3 (user, assistant, user)", got)
	}
	if got := messageRoles(c.Messages()); strings.Join(got, ",") != "user,assistant,user,assistant" {
		t.Errorf("roles = %v", got)
	}
}

// TestConductor_SeedsHistoryFromOptions covers the resume path's entry point:
// a conductor built with History sends that history on the very first turn.
func TestConductor_SeedsHistoryFromOptions(t *testing.T) {
	client := &connectortest.Fake{ProviderName: "p", ModelName: "m",
		Turns: [][]stream.Event{{stream.Finish{}}}}
	seed := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "earlier"}}},
		{Role: "assistant", Content: []connector.ContentBlock{{Type: "text", Text: "sure"}}},
	}
	c := New(Options{Client: client, Sink: &recorder{}, History: seed})

	if _, err := c.Submit(context.Background(), "now"); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if got := len(client.Requests()[0].Messages); got != 3 {
		t.Fatalf("first request had %d messages, want 3", got)
	}
}

// ---------------------------------------------------------------------------
// Interrupt
// ---------------------------------------------------------------------------

// TestConductor_InterruptCancelsInFlightTurn is the frontend-free version of
// "user pressed ESC": another goroutine calls Interrupt while Submit is
// blocked on the model, and Submit returns context.Canceled.
func TestConductor_InterruptCancelsInFlightTurn(t *testing.T) {
	client := &connectortest.Fake{ProviderName: "p", ModelName: "m", BlockUntilCancel: true}
	c := New(Options{Client: client, Sink: &recorder{}, Config: agent.Config{}})

	done := make(chan error, 1)
	go func() {
		_, err := c.Submit(context.Background(), "take your time")
		done <- err
	}()

	// Spin until the turn is actually in flight, then interrupt it.
	for {
		c.mu.Lock()
		running := c.cancel != nil
		c.mu.Unlock()
		if running {
			break
		}
	}
	c.Interrupt()

	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Submit err = %v, want context.Canceled", err)
	}
	// The user turn stays in the conversation: an interrupted prompt is
	// still something the user said, and every frontend relies on being
	// able to keep talking from there.
	if got := messageRoles(c.Messages()); strings.Join(got, ",") != "user" {
		t.Errorf("roles after interrupt = %v, want [user]", got)
	}
}

// TestConductor_InterruptIdleIsNoop guards the obvious foot-gun: a frontend
// that wires a key to Interrupt will call it while nothing is running.
func TestConductor_InterruptIdleIsNoop(t *testing.T) {
	client := &connectortest.Fake{ProviderName: "p", ModelName: "m",
		Turns: [][]stream.Event{{stream.TextDelta{Text: "fine"}, stream.Finish{}}}}
	c := New(Options{Client: client, Sink: &recorder{}})

	c.Interrupt()
	if _, err := c.Submit(context.Background(), "hi"); err != nil {
		t.Fatalf("Submit after idle Interrupt: %v", err)
	}
}

// ---------------------------------------------------------------------------
// SwitchModel
// ---------------------------------------------------------------------------

// TestConductor_SwitchModelUsesResolver proves the boundary: the conductor
// changes models through an interface it declares itself, with no provider
// catalog anywhere in sight.
func TestConductor_SwitchModelUsesResolver(t *testing.T) {
	first := &connectortest.Fake{ProviderName: "p1", ModelName: "m1",
		Turns: [][]stream.Event{{stream.Finish{}}}}
	second := &connectortest.Fake{ProviderName: "p2", ModelName: "m2",
		Turns: [][]stream.Event{{stream.Finish{}}}}
	c := New(Options{
		Client:   first,
		Sink:     &recorder{},
		Resolver: mapResolver{models: map[string]connector.ModelClient{"p2/m2": second}},
	})

	if c.Model() != "m1" || c.Provider() != "p1" {
		t.Fatalf("start = %s/%s, want p1/m1", c.Provider(), c.Model())
	}
	if err := c.SwitchModel("p2/m2"); err != nil {
		t.Fatalf("SwitchModel: %v", err)
	}
	if c.Model() != "m2" || c.Provider() != "p2" {
		t.Errorf("after switch = %s/%s, want p2/m2", c.Provider(), c.Model())
	}
	if _, err := c.Submit(context.Background(), "hi"); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if first.Calls() != 0 || second.Calls() != 1 {
		t.Errorf("calls: first=%d second=%d, want 0 and 1", first.Calls(), second.Calls())
	}
}

// TestConductor_SwitchModelKeepsConversation locks the "mid-conversation model
// change" contract: history and usage survive the switch.
func TestConductor_SwitchModelKeepsConversation(t *testing.T) {
	first := &connectortest.Fake{ProviderName: "p1", ModelName: "m1",
		Turns: [][]stream.Event{{stream.TextDelta{Text: "a"}, stream.Finish{Usage: stream.Usage{Input: 5}}}}}
	second := &connectortest.Fake{ProviderName: "p2", ModelName: "m2",
		Turns: [][]stream.Event{{stream.Finish{}}}}
	c := New(Options{
		Client:   first,
		Sink:     &recorder{},
		Resolver: mapResolver{models: map[string]connector.ModelClient{"p2/m2": second}},
	})

	if _, err := c.Submit(context.Background(), "hello"); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	before := len(c.Messages())
	if err := c.SwitchModel("p2/m2"); err != nil {
		t.Fatalf("SwitchModel: %v", err)
	}
	if got := len(c.Messages()); got != before {
		t.Errorf("history length changed on model switch: %d then %d", before, got)
	}
	if c.Usage().Input != 5 {
		t.Errorf("usage reset by model switch: %+v", c.Usage())
	}
}

// TestConductor_SwitchModelResolverErrorKeepsClient verifies a failed switch
// leaves the conversation on the model it was already using — the TUI shows
// the old model again precisely because nothing changed underneath.
func TestConductor_SwitchModelResolverErrorKeepsClient(t *testing.T) {
	first := &connectortest.Fake{ProviderName: "p1", ModelName: "m1"}
	c := New(Options{
		Client:   first,
		Sink:     &recorder{},
		Resolver: mapResolver{models: map[string]connector.ModelClient{}},
	})

	err := c.SwitchModel("nope/nope")
	if !errors.Is(err, errNoSuchModel) {
		t.Fatalf("err = %v, want the resolver's own error passed through", err)
	}
	if c.Model() != "m1" {
		t.Errorf("model changed despite the error: %s", c.Model())
	}
}

// TestConductor_SwitchModelWithoutResolver documents what a frontend that
// never offers model switching (one-shot prompt mode) gets back.
func TestConductor_SwitchModelWithoutResolver(t *testing.T) {
	c := New(Options{Client: &connectortest.Fake{ProviderName: "p", ModelName: "m"}, Sink: &recorder{}})
	if err := c.SwitchModel("x/y"); !errors.Is(err, ErrNoResolver) {
		t.Fatalf("err = %v, want ErrNoResolver", err)
	}
}

// ---------------------------------------------------------------------------
// Session ownership
// ---------------------------------------------------------------------------

// TestConductor_SessionOpensOnFirstSubmitNotAtStartup is the regression test
// for the lazy-session property the REPL and the TUI both depend on: opening
// a conductor must not litter the sessions directory for a user who never
// types anything.
func TestConductor_SessionOpensOnFirstSubmitNotAtStartup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lazy.jsonl")

	client := &connectortest.Fake{ProviderName: "prov", ModelName: "mod",
		Turns: [][]stream.Event{{stream.TextDelta{Text: "ok"}, stream.Finish{}}}}
	c := New(Options{Client: client, Sink: &recorder{}, SessionPath: path, WorkDir: dir})

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("session file created at startup: stat err = %v", err)
	}
	if c.Session() != nil {
		t.Fatalf("session opened at startup")
	}

	if _, err := c.Submit(context.Background(), "first prompt"); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if c.Session() == nil {
		t.Fatal("session not opened by the first Submit")
	}
	t.Cleanup(func() { c.EndSession("ok", 0) })

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if !strings.Contains(string(data), "first prompt") {
		t.Errorf("user prompt not written to the session log:\n%s", data)
	}
	if !strings.Contains(string(data), `"model":"mod"`) {
		t.Errorf("session header does not carry the conductor's model:\n%s", data)
	}
}

// TestConductor_NoSessionPathWritesNothing covers --no-session: the conductor
// still runs a full conversation, it just persists nothing.
func TestConductor_NoSessionPathWritesNothing(t *testing.T) {
	client := &connectortest.Fake{ProviderName: "p", ModelName: "m",
		Turns: [][]stream.Event{{stream.TextDelta{Text: "ok"}, stream.Finish{}}}}
	c := New(Options{Client: client, Sink: &recorder{}})

	if _, err := c.Submit(context.Background(), "hi"); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if c.Session() != nil {
		t.Errorf("session opened without a path")
	}
	c.EndSession("ok", 0) // must not panic
}

// TestConductor_EndSessionWritesSessionEndOnce verifies the exit path used by
// every frontend, including the double-call the TUI's /new + outer defer
// produces.
func TestConductor_EndSessionWritesSessionEndOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "end.jsonl")
	client := &connectortest.Fake{ProviderName: "p", ModelName: "m",
		Turns: [][]stream.Event{{stream.Finish{Usage: stream.Usage{Input: 11, Output: 2}}}}}
	c := New(Options{Client: client, Sink: &recorder{}, SessionPath: path, WorkDir: dir})

	if _, err := c.Submit(context.Background(), "hi"); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	c.EndSession("ok", 0)
	c.EndSession("ok", 0) // second call must be a no-op

	data, _ := os.ReadFile(path)
	if n := strings.Count(string(data), `"type":"session_end"`); n != 1 {
		t.Errorf("session_end written %d times, want 1:\n%s", n, data)
	}
	if !strings.Contains(string(data), `"input":11`) {
		t.Errorf("session_end lost the accumulated usage:\n%s", data)
	}
	if c.Session() != nil {
		t.Errorf("session pointer survived EndSession")
	}
}

// TestConductor_ClearHistoryKeepsSession pins the console's /new semantics:
// the conversation is dropped, the log keeps recording into the same file.
func TestConductor_ClearHistoryKeepsSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clear.jsonl")
	client := &connectortest.Fake{ProviderName: "p", ModelName: "m",
		Turns: [][]stream.Event{
			{stream.TextDelta{Text: "a"}, stream.Finish{Usage: stream.Usage{Input: 4}}},
			{stream.Finish{}},
		}}
	c := New(Options{Client: client, Sink: &recorder{}, SessionPath: path, WorkDir: dir})
	if _, err := c.Submit(context.Background(), "one"); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	sess := c.Session()

	c.ClearHistory()
	if len(c.Messages()) != 0 {
		t.Errorf("history survived ClearHistory: %v", messageRoles(c.Messages()))
	}
	if c.Session() != sess {
		t.Errorf("ClearHistory closed or swapped the session")
	}
	if c.Usage().Input != 4 {
		t.Errorf("ClearHistory reset usage: %+v", c.Usage())
	}

	c.ResetUsage()
	if c.Usage() != (stream.Usage{}) {
		t.Errorf("ResetUsage left %+v", c.Usage())
	}

	if _, err := c.Submit(context.Background(), "two"); err != nil {
		t.Fatalf("second Submit: %v", err)
	}
	if got := len(client.Requests()[1].Messages); got != 1 {
		t.Errorf("cleared history leaked into the next request: %d messages", got)
	}
	c.EndSession("ok", 0)
}

// TestConductor_ResumeSwapsSessionAndHistory covers the state swap /resume
// performs in both frontends: the old log is closed out with a session_end,
// the target file is reopened in append mode, and history plus usage are
// replaced.
func TestConductor_ResumeSwapsSessionAndHistory(t *testing.T) {
	dir := t.TempDir()
	livePath := filepath.Join(dir, "live.jsonl")
	oldPath := filepath.Join(dir, "old.jsonl")

	// Seed a session to resume onto.
	seed, err := session.Open(oldPath, dir, "m", "p")
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	_ = seed.WriteMessage("user", []session.ContentBlock{{Type: "text", Text: "history"}}, nil)
	_ = seed.Close()

	client := &connectortest.Fake{ProviderName: "p", ModelName: "m",
		Turns: [][]stream.Event{
			{stream.TextDelta{Text: "a"}, stream.Finish{Usage: stream.Usage{Input: 9}}},
			{stream.Finish{}},
		}}
	c := New(Options{Client: client, Sink: &recorder{}, SessionPath: livePath, WorkDir: dir})
	if _, err := c.Submit(context.Background(), "live prompt"); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	resumed := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "history"}}},
	}
	if err := c.Resume(oldPath, resumed, stream.Usage{Input: 100, Output: 50}); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	// The abandoned log got its session_end before being closed.
	liveData, _ := os.ReadFile(livePath)
	if !strings.Contains(string(liveData), `"type":"session_end"`) {
		t.Errorf("previous session was closed without a session_end:\n%s", liveData)
	}
	if c.SessionPath() != oldPath {
		t.Errorf("SessionPath = %q, want %q", c.SessionPath(), oldPath)
	}
	if c.Usage() != (stream.Usage{Input: 100, Output: 50}) {
		t.Errorf("usage after Resume = %+v", c.Usage())
	}
	if got := messageRoles(c.Messages()); strings.Join(got, ",") != "user" {
		t.Errorf("history after Resume = %v", got)
	}
	if c.Session() == nil || !c.Session().IsResume() {
		t.Errorf("resumed session not reopened in append mode")
	}

	// The next prompt lands in the resumed file, not the abandoned one.
	if _, err := c.Submit(context.Background(), "after resume"); err != nil {
		t.Fatalf("Submit after Resume: %v", err)
	}
	c.EndSession("ok", 0)
	oldData, _ := os.ReadFile(oldPath)
	if !strings.Contains(string(oldData), "after resume") {
		t.Errorf("prompt after Resume did not reach the resumed log:\n%s", oldData)
	}
	liveData, _ = os.ReadFile(livePath)
	if strings.Contains(string(liveData), "after resume") {
		t.Errorf("prompt after Resume still went to the abandoned log:\n%s", liveData)
	}
}

// TestConductor_ResumeOpenErrorKeepsRunning verifies a failed /resume reports
// the error and does not take the conversation down with it.
func TestConductor_ResumeOpenErrorKeepsRunning(t *testing.T) {
	dir := t.TempDir()
	c := New(Options{Client: &connectortest.Fake{ProviderName: "p", ModelName: "m"}, Sink: &recorder{}, WorkDir: dir})
	// A directory is not a session file.
	if err := c.Resume(dir, nil, stream.Usage{}); err == nil {
		t.Fatal("expected an error resuming onto a directory")
	}
	if c.Session() != nil {
		t.Errorf("failed Resume left a session behind")
	}
}

// TestConductor_MaxIterationsIsReturnedNotSwallowed keeps agent.ErrMaxIterations
// reachable through Submit: every frontend treats it as a graceful stop rather
// than an error, and it can only do that if it still gets the sentinel.
func TestConductor_MaxIterationsIsReturnedNotSwallowed(t *testing.T) {
	toolTurn := []stream.Event{
		stream.ToolCallStart{ID: "t", Name: "echo"},
		stream.ToolCall{ID: "t", Name: "echo", Arguments: `{}`},
		stream.Finish{Reason: "tool_calls"},
	}
	client := &connectortest.Fake{ProviderName: "p", ModelName: "m",
		Turns: [][]stream.Event{toolTurn, toolTurn}}
	c := New(Options{
		Client: client,
		Sink:   &recorder{},
		Config: agent.Config{
			MaxIterations: 2,
			Tools:         &scriptedTools{answers: map[string]string{"echo": "ok"}},
			Schema:        json.RawMessage(`[]`),
		},
	})

	_, err := c.Submit(context.Background(), "loop forever")
	if !errors.Is(err, agent.ErrMaxIterations) {
		t.Fatalf("err = %v, want agent.ErrMaxIterations", err)
	}
}
