package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/display"
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
	fmt.Print("\033[2J\033[H")
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
	switch line {
	case "/exit":
		cancel()
		fmt.Println("Bye!")
		return true, true
	case "/new":
		cancel()
		s.conversation = nil
		s.display.End()
		if s.totalUsage.Input > 0 || s.totalUsage.Output > 0 {
			fmt.Fprintf(os.Stderr, "───── new conversation ─────\n")
			line := "📊 Session total: " + display.BuildUsageLineNoTiming(s.totalUsage)
			fmt.Fprintf(os.Stderr, "%s\n", line)
		}
		fmt.Print("\033[2J\033[H")
		return false, true
	}
	return false, false
}
