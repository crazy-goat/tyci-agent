package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	lua "github.com/yuin/gopher-lua"

	"github.com/decodo/tyci-agent/agent"
	"github.com/decodo/tyci-agent/providers"
	"github.com/decodo/tyci-agent/tools"
)

// Engine orchestrates Lua workflow scripts.
type Engine struct {
	L       *lua.LState
	ctx     context.Context
	prompt  string
	runner  agent.Runner
}

// NewEngine creates a new workflow engine.
func NewEngine(ctx context.Context, prompt string, runner agent.Runner) *Engine {
	return &Engine{
		L:      lua.NewState(),
		ctx:    ctx,
		prompt: prompt,
		runner: runner,
	}
}

// Run executes a Lua workflow file.
func (e *Engine) Run(scriptPath string) (string, error) {
	defer e.L.Close()

	// Register tyci API
	e.registerTyciAPI()

	// Set prompt
	e.L.SetGlobal("prompt", lua.LString(e.prompt))

	// Execute the script
	if err := e.L.DoFile(scriptPath); err != nil {
		return "", fmt.Errorf("workflow execution error: %v", err)
	}

	// Get return value
	result := e.L.Get(-1)
	e.L.Pop(1)

	return result.String(), nil
}

// registerTyciAPI registers the tyci global table with all API functions.
func (e *Engine) registerTyciAPI() {
	tyci := e.L.NewTable()

	// tyci.cwd()
	e.L.SetField(tyci, "cwd", e.L.NewFunction(e.luaCwd))

	// tyci.models()
	e.L.SetField(tyci, "models", e.L.NewFunction(e.luaModels))

	// tyci.agents()
	e.L.SetField(tyci, "agents", e.L.NewFunction(e.luaAgents))

	// tyci.run_tool()
	e.L.SetField(tyci, "run_tool", e.L.NewFunction(e.luaRunTool))

	// tyci.new_session()
	e.L.SetField(tyci, "new_session", e.L.NewFunction(e.luaNewSession))

	// tyci.resume_session()
	e.L.SetField(tyci, "resume_session", e.L.NewFunction(e.luaResumeSession))

	e.L.SetGlobal("tyci", tyci)
}

// luaCwd returns the current working directory.
func (e *Engine) luaCwd(L *lua.LState) int {
	cwd, err := os.Getwd()
	if err != nil {
		L.Push(lua.LString(""))
	} else {
		L.Push(lua.LString(cwd))
	}
	return 1
}

// luaModels returns available models.
func (e *Engine) luaModels(L *lua.LState) int {
	providers.RegisterProvidersFromConfig(filepath.Join(os.Getenv("HOME"), ".tyci", "model.json"))
	models := providers.ListModels()
	arr := L.NewTable()
	for i, m := range models {
		arr.RawSetInt(i+1, lua.LString(m))
	}
	L.Push(arr)
	return 1
}

// luaAgents returns configured agent names.
func (e *Engine) luaAgents(L *lua.LState) int {
	// TODO: implement agent listing
	arr := L.NewTable()
	L.Push(arr)
	return 1
}

// luaRunTool runs a built-in tool.
func (e *Engine) luaRunTool(L *lua.LState) int {
	name := L.CheckString(1)
	argsTable := L.OptTable(2, nil)

	// Convert Lua table to Go map
	args := make(map[string]any)
	if argsTable != nil {
		argsTable.ForEach(func(key, value lua.LValue) {
			if keyStr, ok := key.(lua.LString); ok {
				args[string(keyStr)] = convertLuaValueToGo(value)
			}
		})
	}

	result := tools.RunTool(e.ctx, name, args)

	// Convert result to Lua table
	tbl := L.NewTable()
	L.SetField(tbl, "success", lua.LBool(result.Success))
	L.SetField(tbl, "content", lua.LString(result.Content))
	L.SetField(tbl, "error", lua.LString(result.Error))
	L.Push(tbl)
	return 1
}

// luaNewSession creates a new agent session.
func (e *Engine) luaNewSession(L *lua.LState) int {
	model := L.OptString(1, "")

	session := &luaSession{
		engine:  e,
		model:   model,
		messages: []providers.RichMessage{},
	}

	// Create metatable for session methods
	mt := L.NewTable()
	L.SetField(mt, "__index", e.newSessionMethods())

	tbl := L.NewTable()
	L.SetField(tbl, "model", lua.LString(model))
	L.SetField(tbl, "messages", L.NewTable())

	L.SetMetatable(tbl, mt)
	L.Push(tbl)
	return 1
}

// luaResumeSession loads a saved session.
func (e *Engine) luaResumeSession(L *lua.LState) int {
	path := L.CheckString(1)

	// Read session file
	data, err := os.ReadFile(path)
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}

	var sessionData struct {
		Model    string                 `json:"model"`
		Messages []providers.RichMessage `json:"messages"`
	}
	if err := json.Unmarshal(data, &sessionData); err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}

	session := &luaSession{
		engine:   e,
		model:    sessionData.Model,
		messages: sessionData.Messages,
	}

	// Create metatable
	mt := L.NewTable()
	L.SetField(mt, "__index", e.newSessionMethods())

	tbl := L.NewTable()
	L.SetField(tbl, "model", lua.LString(session.model))
	L.SetField(tbl, "messages", L.NewTable())

	L.SetMetatable(tbl, mt)
	L.Push(tbl)
	return 1
}

// newSessionMethods creates the session method table.
func (e *Engine) newSessionMethods() *lua.LTable {
	methods := e.L.NewTable()

	e.L.SetField(methods, "prompt", e.L.NewFunction(e.sessionPrompt))
	e.L.SetField(methods, "add_system", e.L.NewFunction(e.sessionAddSystem))
	e.L.SetField(methods, "await", e.L.NewFunction(e.sessionAwait))
	e.L.SetField(methods, "save", e.L.NewFunction(e.sessionSave))
	e.L.SetField(methods, "messages", e.L.NewFunction(e.sessionMessages))

	return methods
}

// sessionPrompt adds a user message.
func (e *Engine) sessionPrompt(L *lua.LState) int {
	session := checkSession(L)
	msg := L.CheckString(2)

	session.messages = append(session.messages, providers.RichMessage{
		Role: "user",
		Content: []providers.ContentBlock{
			{Type: "text", Text: msg},
		},
	})

	// Update messages table
	msgsTable := L.NewTable()
	for i, m := range session.messages {
		mTbl := L.NewTable()
		L.SetField(mTbl, "role", lua.LString(m.Role))
		if len(m.Content) > 0 {
			L.SetField(mTbl, "content", lua.LString(m.Content[0].Text))
		}
		msgsTable.RawSetInt(i+1, mTbl)
	}
	L.SetField(L.CheckTable(1), "messages", msgsTable)

	return 0
}

// sessionAddSystem adds a system message.
func (e *Engine) sessionAddSystem(L *lua.LState) int {
	session := checkSession(L)
	msg := L.CheckString(2)

	session.messages = append(session.messages, providers.RichMessage{
		Role: "system",
		Content: []providers.ContentBlock{
			{Type: "text", Text: msg},
		},
	})

	return 0
}

// sessionAwait sends messages to LLM and waits for response.
func (e *Engine) sessionAwait(L *lua.LState) int {
	session := checkSession(L)

	// Get provider and model
	provider, modelName, ok := providers.FindModel(session.model)
	if !ok {
		L.Push(lua.LNil)
		L.Push(lua.LString("model not found: " + session.model))
		return 2
	}

	// Create collector
	collector := &responseCollector{}

	// Build config
	systemPrompt := providers.BuildSystemPrompt()
	cfg := agent.Config{
		Model:         modelName,
		System:        systemPrompt,
		MaxRetries:    1,
		MaxIterations: 10,
	}

	// Run agent
	_, err := agent.Run(e.ctx, provider, collector, &session.messages, cfg)
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}

	// Build reply table
	reply := L.NewTable()
	L.SetField(reply, "content", lua.LString(collector.content))
	L.SetField(reply, "thinking", lua.LString(collector.thinking))
	L.SetField(reply, "tools", lua.LNumber(collector.toolCalls))

	usage := L.NewTable()
	L.SetField(usage, "input", lua.LNumber(collector.usage.InputTokens))
	L.SetField(usage, "output", lua.LNumber(collector.usage.OutputTokens))
	L.SetField(reply, "usage", usage)

	L.Push(reply)
	return 1
}

// sessionSave saves the session to a file.
func (e *Engine) sessionSave(L *lua.LState) int {
	session := checkSession(L)
	path := L.CheckString(2)

	sessionData := struct {
		Model    string                 `json:"model"`
		Messages []providers.RichMessage `json:"messages"`
	}{
		Model:    session.model,
		Messages: session.messages,
	}

	data, err := json.MarshalIndent(sessionData, "", "  ")
	if err != nil {
		L.Push(lua.LBool(false))
		L.Push(lua.LString(err.Error()))
		return 2
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		L.Push(lua.LBool(false))
		L.Push(lua.LString(err.Error()))
		return 2
	}

	L.Push(lua.LBool(true))
	return 1
}

// sessionMessages returns the message history.
func (e *Engine) sessionMessages(L *lua.LState) int {
	session := checkSession(L)

	msgsTable := L.NewTable()
	for i, m := range session.messages {
		mTbl := L.NewTable()
		L.SetField(mTbl, "role", lua.LString(m.Role))
		if len(m.Content) > 0 {
			L.SetField(mTbl, "content", lua.LString(m.Content[0].Text))
		}
		msgsTable.RawSetInt(i+1, mTbl)
	}

	L.Push(msgsTable)
	return 1
}

// checkSession extracts the session from a Lua table.
func checkSession(L *lua.LState) *luaSession {
	tbl := L.CheckTable(1)
	// The session is stored as a pointer in the table
	// We use a global registry to store session references
	// For now, we'll use a simpler approach
	return &luaSession{
		engine:   nil, // Will be set by the engine
		model:    L.GetField(tbl, "model").String(),
		messages: []providers.RichMessage{},
	}
}

// luaSession represents a Lua-accessible agent session.
type luaSession struct {
	engine   *Engine
	model    string
	messages []providers.RichMessage
}

// responseCollector collects agent responses.
type responseCollector struct {
	content   string
	thinking  string
	toolCalls int
	usage     providers.Usage
}

func (c *responseCollector) Thinking(text string) {
	c.thinking += text
}

func (c *responseCollector) Text(text string) {
	c.content += text
}

func (c *responseCollector) ToolCallStart(name string) {
	c.toolCalls++
}

func (c *responseCollector) ToolCallDelta(delta string) {}

func (c *responseCollector) ToolCallEnd(name, result string) {}

func (c *responseCollector) ToolBlock(msg string) {}

func (c *responseCollector) Summary(usage providers.Usage, stats providers.Stats) {
	c.usage = usage
}

func (c *responseCollector) Error(err error) {}

func (c *responseCollector) End() {}

// convertLuaValueToGo converts a Lua value to a Go value.
func convertLuaValueToGo(v lua.LValue) any {
	switch val := v.(type) {
	case *lua.LNilType:
		return nil
	case lua.LBool:
		return bool(val)
	case lua.LNumber:
		return float64(val)
	case lua.LString:
		return string(val)
	case *lua.LTable:
		result := make(map[string]any)
		val.ForEach(func(key, value lua.LValue) {
			if keyStr, ok := key.(lua.LString); ok {
				result[string(keyStr)] = convertLuaValueToGo(value)
			}
		})
		return result
	default:
		return val.String()
	}
}

// RunWorkflow executes a Lua workflow script.
func RunWorkflow(ctx context.Context, scriptPath, prompt string, runner agent.Runner) (string, error) {
	engine := NewEngine(ctx, prompt, runner)
	return engine.Run(scriptPath)
}
