package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/display"
	"github.com/decodo/tyci/internal/connect"
	"github.com/decodo/tyci/internal/readline"
	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/session"
	"github.com/decodo/tyci/stream"
	"github.com/decodo/tyci/tools"
)

type interactiveState struct {
	provider    providers.Provider
	modelName   string
	display     display.Display
	historyFile string
	cfg         agent.Config
	baseCtx     context.Context
	sessionPath string
	sessionPtr  *session.Session

	conversation []providers.RichMessage
	totalUsage   stream.Usage
	editor       *readline.LineEditor
}

func runInteractive(provider providers.Provider, modelName string, disp display.Display, historyFile string, cfg agent.Config, baseCtx context.Context, sessionPath string) {
	st := &interactiveState{
		provider: provider, modelName: modelName, display: disp,
		historyFile: historyFile, cfg: cfg, baseCtx: baseCtx, sessionPath: sessionPath,
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
	if s.cfg.Session != nil {
		agent.WriteSessionEnd(s.cfg.Session, "ok", 0, &s.totalUsage)
		if s.sessionPath != "" {
			fmt.Fprintf(os.Stderr, "📁 Session: %s\n", s.sessionPath)
		}
	}
}

func (s *interactiveState) replaySession() {
	if s.cfg.Session == nil || !s.cfg.Session.IsResume() || s.sessionPath == "" {
		return
	}
	parsedLines := s.cfg.Session.Messages()
	rebuiltMsgs, _ := session.RebuildMessages(parsedLines)
	if len(rebuiltMsgs) > 0 {
		s.conversation = rebuiltMsgs
	}

	// Re-render the transcript so the user can scroll back through it.
	// We use replaySessionToDisplay (one ToolBlock per message,
	// kind="block") instead of pushing every text/thinking/toolCall
	// through Display.Text/Thinking: that previous path went through
	// glamour and produced race conditions where selection Y coordinates
	// resolved onto wrong rows during the user's mouse drag, blanking
	// the screen on release. The new path uses pure wrapText — selection
	// highlights track the actual on-screen characters.
	replaySessionToDisplay(s.display, s.sessionPath)

	if len(rebuiltMsgs) > 0 {
		fmt.Fprintf(os.Stderr, "ℹ Resumed session %s (%d messages)\n", s.cfg.Session.ID(), len(rebuiltMsgs))
	}
}

func (s *interactiveState) loop() {
	for {
		iterCtx, iterCancel := context.WithCancel(s.baseCtx)
		line, err := s.readLine(iterCtx)
		if s.handleReadError(err, iterCancel) {
			return
		}
		if err != nil {
			continue
		}
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
		s.submitUserLine(line)
		if fatal := s.runAgentIteration(iterCtx, iterCancel); fatal {
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
		s.conversation = nil
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
	current := s.provider.Name() + "/" + s.modelName
	for _, name := range keys {
		p, ok := providers.GetProvider(name)
		if !ok {
			continue
		}
		models := append([]string{}, p.Models()...)
		models = append(models, p.FreeModels()...)
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
	p, m, ok := providers.FindModel(spec)
	if !ok {
		fmt.Fprintf(os.Stderr, "Model %q not found. Run '/model' to see available models.\n", spec)
		return
	}
	if !p.IsConfigured() {
		fmt.Fprintf(os.Stderr, "Provider %q is not configured. Add a key via 'tyci provider auth set %s <key>'.\n", p.Name(), p.Name())
		return
	}
	s.provider = p
	s.modelName = m
	fmt.Fprintf(os.Stdout, "Switched to %s/%s\n", p.Name(), m)
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

	// Close current session cleanly before swapping.
	if s.sessionPtr != nil {
		_ = s.sessionPtr.Close()
	}
	s.sessionPtr = nil
	s.sessionPath = ""

	wd, _ := os.Getwd()
	newSess, err := session.Open(path, wd, s.modelName, s.provider.Name())
	if err != nil {
		fmt.Fprintf(os.Stderr, "/resume: reopen failed: %v\n", err)
		return
	}
	s.sessionPtr = newSess
	s.sessionPath = path
	s.conversation = msgs
	s.totalUsage = stream.Usage{
		Input:     total.Input,
		Output:    total.Output,
		Reasoning: total.Reasoning,
		CacheRead: total.CacheRead,
	}
	if summary.Provider != "" && summary.Model != "" {
		if p, m, ok := providers.FindModel(summary.Provider + "/" + summary.Model); ok {
			s.provider = p
			s.modelName = m
		}
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
