// Package conductor owns a conversation.
//
// Before this package existed, every frontend kept its own copy of the same
// four things — the message history, the agent configuration, the
// connector.ModelClient in use, and the session log — plus its own copy of
// the loop that ties them together: append the user line, lazily materialize
// the session file, run the agent, accumulate usage, write a session_end on
// the way out. The console, the TUI and one-shot prompt mode each had that
// logic spelled out again, and they had already drifted apart.
//
// A Conductor holds that state once. Frontends tell it what to do (Submit,
// Interrupt, SwitchModel, Resume) and render whatever the agent loop pushes
// into the agent.Sink they supplied. Everything a frontend decides — what an
// error looks like on screen, whether to print a trailing newline, which key
// means "stop" — stays in the frontend. The Conductor returns a result and an
// error; it never writes to the terminal.
//
// The decisive property, and the point of the whole exercise: a Conductor
// runs a complete conversation with no frontend at all. See
// TestConductor_HeadlessConversation.
//
// The package deliberately does NOT import providers. Changing the model goes
// through ModelResolver, an interface declared here and implemented by the
// caller, so the provider catalog stays on the CLI side of the boundary —
// the same shape as agent.Sink and connector.HTTPDoer.
package conductor

import (
	"context"
	"errors"
	"sync"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/session"
	"github.com/decodo/tyci/stream"
)

// ModelResolver turns a user-supplied model spec into a ready-to-use client.
//
// It exists so the Conductor can switch models without knowing that a
// provider catalog exists. The CLI implements it on top of
// providers.FindModel; a test implements it with a map. Errors are passed
// through to the caller untouched, so a frontend can match on its own error
// types and render its own wording.
type ModelResolver interface {
	Resolve(spec string) (connector.ModelClient, error)
}

// ErrNoResolver is returned by SwitchModel when the Conductor was built
// without a ModelResolver. A frontend that never offers model switching
// (one-shot prompt mode) can leave Options.Resolver nil.
var ErrNoResolver = errors.New("conductor: no model resolver configured")

// ErrTurnInFlight is returned by a Submit that arrives while another Submit is
// still running. One conversation runs one turn at a time.
//
// It is an error rather than a wait on purpose. A mutex would quietly
// serialize the two calls, and the frontend bug that produced them would show
// up as an unexplained hang instead of a message; a queue is a feature nobody
// asked for, with its own questions about ordering and cancellation. An
// immediate error says exactly what is wrong at the moment it goes wrong.
var ErrTurnInFlight = errors.New("conductor: a turn is already in flight")

// Options is everything a Conductor needs at construction time.
type Options struct {
	// Client is the model client the conversation starts on. Required.
	Client connector.ModelClient

	// Sink receives every event the agent loop produces. Required. Any
	// display implementation satisfies it structurally.
	Sink agent.Sink

	// Config is the agent configuration. Config.Session is the session log
	// the conversation starts with (nil for the lazy path, non-nil when the
	// caller already opened an explicit --session); the Conductor takes
	// ownership of it from here on and no caller should keep a second
	// pointer to it.
	Config agent.Config

	// Resolver backs SwitchModel. Optional.
	Resolver ModelResolver

	// History seeds the conversation, e.g. with a resumed transcript.
	History []connector.Message

	// SessionPath is where the session log lives. When Config.Session is nil
	// and SessionPath is non-empty, the file is opened on the first Submit
	// and not before — an empty JSONL per abandoned REPL is exactly what
	// that laziness prevents. Empty disables session persistence.
	SessionPath string

	// WorkDir is recorded in the session header. Empty means os.Getwd().
	WorkDir string
}

// Conductor drives one conversation.
//
// All methods must be called from the goroutine that drives the conversation,
// with two exceptions:
//
//   - Interrupt is safe to call from any goroutine at any time, because "stop
//     what you are doing" is by definition something a frontend needs to say
//     while Submit is still running.
//   - Submit defends itself: a second, concurrent Submit is rejected with
//     ErrTurnInFlight instead of corrupting the conversation. That is a
//     diagnostic, not a license — everything else here (Messages, Usage,
//     SetHistory, SwitchModel, the session calls) still belongs to the one
//     goroutine driving the conversation.
type Conductor struct {
	client   connector.ModelClient
	sink     agent.Sink
	cfg      agent.Config
	resolver ModelResolver

	conversation []connector.Message
	usage        stream.Usage
	sessionPath  string
	workDir      string

	// mu guards cancel and inFlight — the only fields touched from a second
	// goroutine (Interrupt, or a stray concurrent Submit) while a turn is
	// running.
	mu       sync.Mutex
	cancel   context.CancelFunc
	inFlight bool
}

// New builds a Conductor over opts.
func New(opts Options) *Conductor {
	return &Conductor{
		client:       opts.Client,
		sink:         opts.Sink,
		cfg:          opts.Config,
		resolver:     opts.Resolver,
		conversation: opts.History,
		sessionPath:  opts.SessionPath,
		workDir:      opts.WorkDir,
	}
}

// Submit records prompt as a user turn and runs the agent loop over the
// conversation until the model stops asking for tools.
//
// It returns the usage of this turn (the running total is available from
// Usage) and whatever agent.Run returned. Two error values are not failures
// and every frontend treats them as such: context.Canceled means the user
// interrupted, and agent.ErrMaxIterations means the loop hit its cap after
// the agent already warned about it through the Sink.
//
// A Submit that arrives while another one is running returns ErrTurnInFlight
// and does nothing else: no user turn appended, no session line written, no
// model call. The claim is taken before the first mutation precisely so that a
// rejected Submit leaves no trace to clean up.
func (c *Conductor) Submit(ctx context.Context, prompt string) (stream.Usage, error) {
	// Claim the turn: test and set in one critical section, so two callers
	// racing here cannot both come away believing they won.
	c.mu.Lock()
	if c.inFlight {
		c.mu.Unlock()
		return stream.Usage{}, ErrTurnInFlight
	}
	c.inFlight = true
	c.mu.Unlock()
	// Released in a defer registered immediately, so neither an early return
	// below nor a panic in the agent loop can strand the conversation with a
	// turn that never ends.
	defer func() {
		c.mu.Lock()
		c.inFlight = false
		c.mu.Unlock()
	}()

	c.conversation = append(c.conversation, connector.Message{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: prompt}},
	})

	// Materialize the session file now — on the first prompt the user
	// actually submits, never at startup.
	c.EnsureSession()
	if c.cfg.Session != nil {
		blocks := []session.ContentBlock{{Type: "text", Text: prompt}}
		_ = c.cfg.Session.WriteMessage("user", blocks, nil)
	}

	runCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()
	defer func() {
		cancel()
		c.mu.Lock()
		c.cancel = nil
		c.mu.Unlock()
	}()

	usage, err := agent.Run(runCtx, c.client, c.sink, &c.conversation, c.cfg)
	c.usage.Add(usage)
	return usage, err
}

// Interrupt cancels the turn currently in flight. It is a no-op when nothing
// is running, and safe from any goroutine.
//
// Which key or signal means "interrupt" is not the Conductor's business: the
// console wires SIGINT and ESC to it, the TUI wires its own cancel channel.
func (c *Conductor) Interrupt() {
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// SwitchModel points the conversation at a different model, resolved through
// the ModelResolver the caller supplied. The conversation, the session log
// and the accumulated usage are untouched — this is a mid-conversation model
// change, not a new conversation.
//
// The resolver's error is returned verbatim so the frontend can match it.
func (c *Conductor) SwitchModel(spec string) error {
	if c.resolver == nil {
		return ErrNoResolver
	}
	mc, err := c.resolver.Resolve(spec)
	if err != nil {
		return err
	}
	c.client = mc
	return nil
}

// Model is the bare model name currently in use.
func (c *Conductor) Model() string { return c.client.Model() }

// Provider names the provider serving the current model.
func (c *Conductor) Provider() string { return c.client.Provider() }

// Usage is the usage accumulated across every turn since the Conductor was
// built (or since the last ResetUsage / Resume).
func (c *Conductor) Usage() stream.Usage { return c.usage }

// Messages is the live conversation. The slice is the one the agent loop
// appends to, so callers should read it, not retain it.
func (c *Conductor) Messages() []connector.Message { return c.conversation }

// Client is the connector.ModelClient currently driving this conversation.
// Exposed so a caller can fork the conversation onto an independent run
// (e.g. the TUI's /btw side-conversation) without duplicating model
// resolution logic that already lives here.
func (c *Conductor) Client() connector.ModelClient { return c.client }

// Config is the agent.Config this conductor drives Submit with. Exposed for
// the same reason as Client: a caller forking the conversation (e.g. /btw)
// wants the same tool/schema/fallback behavior, minus the callbacks that are
// specific to this conductor's own conversation (Session, NextMessages,
// PendingTodos, HasTodos) — which it strips out itself, using this as the
// base.
func (c *Conductor) Config() agent.Config { return c.cfg }

// SetNextMessages replaces the callback the agent loop calls between
// iterations to pick up anything that arrived mid-turn. Exposed because the
// frontend only learns what it wants to service there after the Conductor
// exists — the TUI wraps its own drain around the one Options supplied.
//
// Whatever is installed here runs on the agent's goroutine at a point where
// the conversation is not being appended to, which is what makes it a safe
// place to read the history from (see the TUI's mid-turn /btw).
func (c *Conductor) SetNextMessages(fn func() []string) { c.cfg.NextMessages = fn }

// SetHistory replaces the conversation wholesale. Intended for seeding a
// fresh Conductor with a transcript the caller has already rebuilt from a
// session file.
func (c *Conductor) SetHistory(msgs []connector.Message) { c.conversation = msgs }

// ClearHistory drops the conversation and leaves everything else alone —
// notably the session log, which keeps recording into the same file. This is
// the console's /new.
func (c *Conductor) ClearHistory() { c.conversation = nil }

// ResetUsage zeroes the running usage total. The TUI's /new does this
// together with ClearHistory and EndSession; the console's /new does not,
// because it keeps writing to the same session file and the session_end
// event should still report everything that file recorded.
func (c *Conductor) ResetUsage() { c.usage = stream.Usage{} }

// Session is the open session log, or nil when there is none.
func (c *Conductor) Session() *session.Session { return c.cfg.Session }

// SessionPath is the path of the session log, open or not yet opened.
func (c *Conductor) SessionPath() string { return c.sessionPath }

// EnsureSession opens the session log if it is not open yet and returns it
// (nil when session persistence is off or the file could not be opened).
//
// Submit calls this itself; it is exported for the one caller that has to
// look at the session before the first prompt — one-shot prompt mode needs
// IsResume() in order to decide whether to prepend a transcript.
func (c *Conductor) EnsureSession() *session.Session {
	sess, path, _ := ensureLazySession(c.cfg.Session, c.sessionPath, c.workDir, c.client.Model(), c.client.Provider())
	c.cfg.Session = sess
	if sess != nil {
		c.sessionPath = path
	}
	return sess
}

// EndSession writes a session_end event, closes the log and forgets it. A
// later Submit will lazily open SessionPath again, which is what makes
// "end the session, keep talking" work for the TUI's /new.
//
// Safe to call with no session open.
func (c *Conductor) EndSession(status string, exitCode int) {
	if c.cfg.Session == nil {
		return
	}
	agent.WriteSessionEnd(c.cfg.Session, status, exitCode, &c.usage)
	c.cfg.Session = nil
}

// Resume swaps the live conversation onto a previously recorded session file.
//
// The caller has already read that file — it needs the summary, the rebuilt
// messages and the corrupt-line count for its own rendering — and passes the
// results in. Resume performs only the state swap that has to happen as one
// step: close out the current log, reopen the target in append mode, and
// replace the history and the usage total.
//
// Note the ordering: the current session's session_end is written BEFORE its
// Close, because WriteSessionEnd refuses to encode into a closed writer, and
// a later EndSession on exit must find nothing left to do.
//
// The new log is opened under the model in use at the time of the call. A
// caller that wants to follow the recorded session back to its original model
// calls SwitchModel afterwards — that is a separate decision, and the two
// frontends make it differently.
//
// Resume renders nothing. Replaying the transcript, reporting corrupt lines
// and announcing "Resumed session ..." are the frontend's business.
func (c *Conductor) Resume(path string, msgs []connector.Message, usage stream.Usage) error {
	c.EndSession("ok", 0)

	sess, err := session.Open(path, normalizeCWD(c.workDir), c.client.Model(), c.client.Provider())
	if err != nil {
		return err
	}
	c.cfg.Session = sess
	c.sessionPath = path
	c.conversation = msgs
	c.usage = usage
	return nil
}
