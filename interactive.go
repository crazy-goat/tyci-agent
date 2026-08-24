package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/decodo/tyci/conductor"
	"github.com/decodo/tyci/display"
	"github.com/decodo/tyci/internal/connect"
	"github.com/decodo/tyci/internal/readline"
	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/session"
	"github.com/decodo/tyci/stream"
	"github.com/decodo/tyci/tools"
)

// interactiveState is the console REPL. It owns the line editor, the slash
// commands and everything printed to the terminal; the conversation itself —
// history, model client, session log, usage — belongs to the conductor.
type interactiveState struct {
	cond        *conductor.Conductor
	display     display.Display
	historyFile string
	baseCtx     context.Context

	editor *readline.LineEditor
}

func runInteractive(cond *conductor.Conductor, disp display.Display, historyFile string, baseCtx context.Context) {
	st := &interactiveState{
		cond: cond, display: disp,
		historyFile: historyFile, baseCtx: baseCtx,
	}
	st.init()
	defer st.close()
	st.loop()
}

func (s *interactiveState) init() {
	s.replaySession()
	if s.historyFile == "" {
		return
	}
	editor, err := readline.New(s.historyFile, readline.DefaultMaxEntries)
	if err != nil {
		fmt.Fprintf(os.Stdout, "Warning: cannot init readline: %v\n", err)
		return
	}
	s.editor = editor
}

func (s *interactiveState) close() {
	if s.editor != nil {
		s.editor.Close()
	}
	if s.cond.Session() != nil {
		s.cond.EndSession("ok", 0)
		if path := s.cond.SessionPath(); path != "" {
			fmt.Fprintf(os.Stderr, "📁 Session: %s\n", path)
		}
	}
}

func (s *interactiveState) replaySession() {
	sess := s.cond.Session()
	if sess == nil || !sess.IsResume() || s.cond.SessionPath() == "" {
		return
	}
	parsedLines := sess.Messages()
	rebuiltMsgs, _ := session.RebuildMessages(parsedLines)
	if len(rebuiltMsgs) > 0 {
		s.cond.SetHistory(rebuiltMsgs)
	}

	// Re-render the transcript so the user can scroll back through it.
	// We use replaySessionToDisplay (one ToolBlock per message,
	// kind="block") instead of pushing every text/thinking/toolCall
	// through Display.Text/Thinking: that previous path went through
	// glamour and produced race conditions where selection Y coordinates
	// resolved onto wrong rows during the user's mouse drag, blanking
	// the screen on release. The new path uses pure wrapText — selection
	// highlights track the actual on-screen characters.
	replaySessionToDisplay(s.display, s.cond.SessionPath())

	if len(rebuiltMsgs) > 0 {
		fmt.Fprintf(os.Stderr, "ℹ Resumed session %s (%d messages)\n", sess.ID(), len(rebuiltMsgs))
	}
}

func (s *interactiveState) loop() {
	for {
		readCtx, readCancel := context.WithCancel(s.baseCtx)
		notice := make(chan struct{}, 1)
		watchDone := make(chan struct{})
		// readline blocks while idle, so unlike the TUI it cannot select on
		// JobNotices itself. Wake it when a background subagent finishes; the
		// notice then becomes the next model turn instead of waiting for the
		// person to type something.
		go func() {
			select {
			case <-JobNotices.Signal():
				select {
				case notice <- struct{}{}:
				default:
				}
				readCancel()
			case <-watchDone:
			}
		}()
		line, err := s.readLine(readCtx)
		close(watchDone)
		readCancel()
		select {
		case <-notice:
			notices := JobNotices.Drain()
			if len(notices) == 0 {
				continue
			}
			line = strings.Join(notices, "\n")
			err = nil
		default:
		}
		if err != nil {
			cancel := func() {}
			if s.handleReadError(err, cancel) {
				return
			}
			continue
		}

		iterCtx, iterCancel := context.WithCancel(s.baseCtx)
		if exit, handled := s.handleCommand(line, iterCancel); exit {
			return
		} else if handled {
			continue
		}
		line = strings.TrimSpace(line)
		if line == "" {
			iterCancel()
			continue
		}
		if s.editor != nil {
			s.editor.AddHistory(line)
		}
		if fatal := s.runAgentIteration(iterCtx, iterCancel, line); fatal {
			return
		}
	}
}

func (s *interactiveState) readLine(ctx context.Context) (string, error) {
	if s.editor != nil {
		return s.editor.Read(ctx, ">>> (Alt+Enter to send) ")
	}
	return simplePrompt(">>> ")
}

func (s *interactiveState) handleReadError(err error, cancel context.CancelFunc) bool {
	if errors.Is(err, readline.ErrEOF) {
		cancel()
		fmt.Println("Bye!")
		return true
	}
	if err == nil {
		return false
	}
	cancel()
	if errors.Is(err, context.Canceled) || errors.Is(err, readline.ErrInterrupt) {
		fmt.Fprint(os.Stdout, "\n")
		return false
	}
	fmt.Fprintf(os.Stdout, "Read error: %v\n", err)
	return false
}

func (s *interactiveState) handleCommand(raw string, cancel context.CancelFunc) (exit bool, handled bool) {
	// " /cmd" (space before /) is NOT a command — only inputs starting
	// exactly with "/" are treated as slash commands.
	if !strings.HasPrefix(raw, "/") {
		return false, false
	}
	line := strings.TrimSpace(raw)
	switch {
	case line == "/exit":
		cancel()
		fmt.Println("Bye!")
		return true, true
	case line == "/new":
		cancel()
		// Console /new drops the conversation but keeps writing to the
		// same session file — deliberately unlike the TUI, which starts a
		// fresh log. Preserved as-is; the difference is now visible in the
		// two conductor calls rather than buried in two copies of a loop.
		s.cond.ClearHistory()
		tools.ClearTodoList()
		fmt.Print("\033[2J\033[H")
		return false, true
	case line == "/resume" || strings.HasPrefix(line, "/resume "):
		arg := strings.TrimSpace(strings.TrimPrefix(line, "/resume"))
		s.handleResume(arg)
		return false, true
	case line == "/model" || strings.HasPrefix(line, "/model "):
		cancel()
		s.handleModelCommand(strings.TrimSpace(strings.TrimPrefix(line, "/model")))
		return false, true
	default:
		cmd := strings.Fields(line)[0]
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		return false, true
	}
}

func (s *interactiveState) handleModelCommand(arg string) {
	if arg == "" {
		s.listAvailableModels()
		return
	}
	s.switchModel(arg)
}

func (s *interactiveState) listAvailableModels() {
	keys, err := connect.ListKeys()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading auth.json: %v\n", err)
		return
	}
	if len(keys) == 0 {
		fmt.Fprintln(os.Stdout, "No providers configured in auth.json. Use 'tyci provider auth set <provider> <key>' to add one.")
		return
	}

	fmt.Fprintln(os.Stdout, "Available models (providers configured in auth.json):")
	fmt.Fprintln(os.Stdout, "")
	any := false
	current := s.cond.Provider() + "/" + s.cond.Model()
	for _, name := range keys {
		p, ok := providers.GetProvider(name)
		if !ok {
			continue
		}
		models := append([]string{}, p.Models()...)
		if len(models) == 0 {
			continue
		}
		any = true
		fmt.Fprintf(os.Stdout, "  %s\n", name)
		for _, m := range models {
			marker := "  "
			full := name + "/" + m
			if full == current {
				marker = "▶ "
			}
			fmt.Fprintf(os.Stdout, "    %s%s\n", marker, full)
		}
	}
	if !any {
		fmt.Fprintln(os.Stdout, "  (no models registered for configured providers)")
	}
	fmt.Fprintln(os.Stdout, "")
	fmt.Fprintln(os.Stdout, "Current: "+current)
	fmt.Fprintln(os.Stdout, "Usage:   /model provider/model-name")
}

func (s *interactiveState) switchModel(spec string) {
	spec = strings.TrimSpace(spec)
	if !strings.Contains(spec, "/") {
		fmt.Fprintf(os.Stderr, "Invalid model %q: expected 'provider/model-name'\n", spec)
		return
	}
	if err := s.cond.SwitchModel(spec); err != nil {
		var notFound *modelNotFoundError
		var notConfigured *providerNotConfiguredError
		switch {
		case errors.As(err, &notFound):
			fmt.Fprintf(os.Stderr, "Model %q not found. Run '/model' to see available models.\n", spec)
		case errors.As(err, &notConfigured):
			fmt.Fprintf(os.Stderr, "Provider %q is not configured. Add a key via 'tyci provider auth set %s <key>'.\n", notConfigured.provider, notConfigured.provider)
		default:
			fmt.Fprintf(os.Stderr, "/model: %v\n", err)
		}
		return
	}
	_, _ = fmt.Fprintf(os.Stdout, "Switched to %s/%s\n", s.cond.Provider(), s.cond.Model())
}

func (s *interactiveState) handleResume(arg string) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		fmt.Fprintln(os.Stderr, "Usage: /resume <index|path>")
		return
	}
	path, err := resolveSessionRef(".", arg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "/resume: %v\n", err)
		return
	}
	summary, msgs, total, corrupt, err := session.LoadForReplay(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "/resume: %v\n", err)
		return
	}
	if len(corrupt) > 0 {
		fmt.Fprintf(os.Stderr, "/resume: %d corrupt lines skipped\n", len(corrupt))
	}

	if err := s.cond.Resume(path, msgs, stream.Usage{
		Input:     total.Input,
		Output:    total.Output,
		Reasoning: total.Reasoning,
		CacheRead: total.CacheRead,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "/resume: reopen failed: %v\n", err)
		return
	}
	// Follow the resumed session back to the model it was recorded with,
	// when that model still resolves. A failure here is not worth a message:
	// the conversation continues on the model already in use.
	if summary.Provider != "" && summary.Model != "" {
		_ = s.cond.SwitchModel(summary.Provider + "/" + summary.Model)
	}

	// Render the swapped-in transcript so the user can scroll it.
	// We use the deterministic block-per-message replay path so the
	// visible lines match exactly what was drawn — necessary for
	// mouse selection across lines and PgUp/PgDown anchoring.
	replaySessionToDisplay(s.display, path)
	fmt.Fprintf(os.Stdout, "\nResumed session %s (%d messages, usage in/out: %d/%d)\n",
		summary.ID, len(msgs), total.Input, total.Output)
	fmt.Printf("\033]133;C\007")
}
