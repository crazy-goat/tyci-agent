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
)

type interactiveState struct {
	provider    providers.Provider
	modelName   string
	display     display.Display
	historyFile string
	cfg         agent.Config
	baseCtx     context.Context
	sessionPath string

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
	replaySessionToDisplay(s.display, s.sessionPath)
	parsedLines := s.cfg.Session.Messages()
	rebuiltMsgs, _ := session.RebuildMessages(parsedLines)
	if len(rebuiltMsgs) > 0 {
		s.conversation = rebuiltMsgs
		fmt.Fprintf(os.Stderr, "ℹ Resumed session %s (%d messages)\n", s.cfg.Session.ID(), len(s.conversation))
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
		line = strings.TrimSpace(line)
		if exit, handled := s.handleCommand(line, iterCancel); exit {
			return
		} else if handled {
			continue
		}
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

func (s *interactiveState) handleCommand(line string, cancel context.CancelFunc) (exit bool, handled bool) {
	switch {
	case line == "/exit":
		cancel()
		fmt.Println("Bye!")
		return true, true
	case line == "/new":
		cancel()
		s.conversation = nil
		fmt.Print("\033[2J\033[H")
		return false, true
	case line == "/model" || strings.HasPrefix(line, "/model "):
		cancel()
		s.handleModelCommand(strings.TrimSpace(strings.TrimPrefix(line, "/model")))
		return false, true
	}
	return false, false
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
		fmt.Fprintln(os.Stdout, "No providers configured in auth.json. Use 'tyci-agent provider auth set <provider> <key>' to add one.")
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
		fmt.Fprintf(os.Stderr, "Provider %q is not configured. Add a key via 'tyci-agent provider auth set %s <key>'.\n", p.Name(), p.Name())
		return
	}
	s.provider = p
	s.modelName = m
	s.cfg.Model = m
	s.cfg.ProviderName = p.Name()
	fmt.Fprintf(os.Stdout, "Switched to %s/%s\n", p.Name(), m)
}
