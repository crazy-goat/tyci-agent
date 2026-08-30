package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	lua "github.com/yuin/gopher-lua"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/internal/agentdefs"
	"github.com/decodo/tyci/internal/connect"
	"github.com/decodo/tyci/internal/ledger"
	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/stream"
	"github.com/decodo/tyci/tools"
)

// defaultSessionMaxIterations is the tool-call iteration cap a session gets
// when nothing else specifies one: neither an explicit max_iterations option
// nor a named agent (whose frontmatter can set max_iterations) was passed to
// tyci.new_session/resume_session. It matches the value this package
// hardcoded before MaxIterations became configurable.
const defaultSessionMaxIterations = 10

// Engine orchestrates Lua workflow scripts.
type Engine struct {
	L        *lua.LState
	ctx      context.Context
	prompt   string
	sessions map[string]*luaSession

	// WorkDir is the directory named-agent lookups (tyci.agents(),
	// tyci.new_session/resume_session's opts.agent) resolve project-local
	// definitions from. Empty means "the process's actual current
	// directory" (os.Getwd(), via agentdefs' own default) — the behavior
	// before this field existed. The CLI entry point (workflowcmd.go) sets
	// it from --dir so a script run with an explicit --dir resolves agent
	// definitions from the same directory its own script discovery used,
	// instead of silently falling back to the process cwd.
	WorkDir string
}

// NewEngine creates a new workflow engine.
//
// The Lua state is stripped of everything that would let a script act on
// the machine directly (RestrictLuaStdlib — os.execute, io.open, require,
// etc.) and given ctx as its cancellation context (L.SetContext), the same
// two protections tools/lua_eval.go's "lua" tool applies to a model-written
// script: filesystem/process access must go through tyci.run_tool (which
// applies the runtime tool gate and the write-freshness guard — see
// tools.RunTool), and a runaway `while true do end` in a project-local
// .tyci/agents/*.lua script can be cancelled instead of hanging the
// process. Before this, a workflow script ran with the full gopher-lua
// standard library open and no cancellation hook at all.
//
// pre_tool/post_tool hooks (.tyci/hooks.json), project-local Lua *tools*
// (.tyci/tools/*.lua — distinct from the *.lua orchestration scripts this
// package runs), the local cron dir, and MCP servers (.tyci/mcp.json) are
// NOT this constructor's concern: `tyci workflow run` (workflowcmd.go, the
// only caller of NewEngine in this repo — the exported RunWorkflow below
// also calls it, but has no caller of its own today) wires all four up
// itself, trust-gated the same way main.go's initCommon does for
// `run`/`console`/`tui`/cron (commands.go's setupProjectLocalEnv, shared by
// both), before ever calling NewEngine — so by the time a script's
// tyci.run_tool reaches tools.RunTool via e.ctx, the same project-local
// content initCommon would have loaded is already in place (or correctly
// absent, for an untrusted project). A caller of NewEngine that skips that
// setup (as this package's own tests mostly do, deliberately, to stay
// independent of it) gets only what loads unconditionally: every BUILT-IN
// tool and any global ~/.tyci/tools Lua tool.
func NewEngine(ctx context.Context, prompt string) *Engine {
	L := lua.NewState()
	tools.RestrictLuaStdlib(L)
	L.SetContext(ctx)
	return &Engine{
		L:        L,
		ctx:      ctx,
		prompt:   prompt,
		sessions: make(map[string]*luaSession),
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

	// tyci.subagent() / tyci.wait() — fan-out sugar so a script doesn't have
	// to hand-roll tyci.run_tool("subagent", ...) / tyci.run_tool("wait", ...).
	e.L.SetField(tyci, "subagent", e.L.NewFunction(e.luaSubagent))
	e.L.SetField(tyci, "wait", e.L.NewFunction(e.luaWait))

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

// luaModels returns available providers.
func (e *Engine) luaModels(L *lua.LState) int {
	_ = connect.EnsureProvidersJSON()
	_ = providers.RegisterProvidersFromProvidersJSON(connect.ProvidersJSONPath())
	providers.RegisterProvidersFromConfig(connect.ModelJSONPath())
	providerList := providers.ListProviders()
	arr := L.NewTable()
	for i, p := range providerList {
		arr.RawSetInt(i+1, lua.LString(p.Name()))
	}
	L.Push(arr)
	return 1
}

// luaAgents returns configured agent names.
func (e *Engine) luaAgents(L *lua.LState) int {
	defs := agentdefs.List(e.WorkDir)
	arr := L.NewTable()
	for i, def := range defs {
		arr.RawSetInt(i+1, lua.LString(def.Name))
	}
	L.Push(arr)
	return 1
}

// luaRunTool runs a built-in tool.
func (e *Engine) luaRunTool(L *lua.LState) int {
	name := L.CheckString(1)
	args := luaTableToArgs(L.OptTable(2, nil))

	result := tools.RunTool(e.ctx, name, args)
	L.Push(toolResultToLua(L, result))
	return 1
}

// luaSubagent is sugar for tyci.run_tool("subagent", args): spawn one or
// more child agents (opts.task for a single child, opts.tasks for a
// parallel fan-out) without a script having to name the tool itself. Pass
// async=true (or async=true on individual tasks) to get job_id(s) back
// immediately and pair the call with tyci.wait().
func (e *Engine) luaSubagent(L *lua.LState) int {
	argsTable := L.OptTable(1, nil)
	args := luaTableToArgs(argsTable)
	result := tools.RunTool(e.ctx, "subagent", args)
	L.Push(toolResultToLua(L, result))
	return 1
}

// luaWait is sugar for tyci.run_tool("wait", args): block until a
// background job (spawned via tyci.subagent(async=true, ...)) finishes, or
// simply pause. Accepts either a bare job_id string — tyci.wait(job_id) — or
// an options table — tyci.wait({job_id=..., seconds=...}) — matching the
// "wait" tool's own job_id/seconds parameters.
func (e *Engine) luaWait(L *lua.LState) int {
	args := make(map[string]any)
	switch v := L.Get(1).(type) {
	case lua.LString:
		args["job_id"] = string(v)
	case lua.LNumber:
		// tyci.wait(30) — a bare plain sleep, matching the "wait" tool's own
		// seconds-only form (job_id omitted).
		args["seconds"] = float64(v)
	case *lua.LTable:
		args = luaTableToArgs(v)
	}

	result := tools.RunTool(e.ctx, "wait", args)
	L.Push(toolResultToLua(L, result))
	return 1
}

// luaTableToArgs converts an optional Lua table of named arguments to a Go
// map, the same conversion luaRunTool has always done for tyci.run_tool's
// own args table — shared here so tyci.subagent/tyci.wait build their
// arguments identically.
func luaTableToArgs(t *lua.LTable) map[string]any {
	args := make(map[string]any)
	if t == nil {
		return args
	}
	t.ForEach(func(key, value lua.LValue) {
		if keyStr, ok := key.(lua.LString); ok {
			args[string(keyStr)] = convertLuaValueToGo(value)
		}
	})
	return args
}

// toolResultToLua converts a tools.ToolResult to the {success, content,
// error} table every tyci.* tool-invoking function pushes.
func toolResultToLua(L *lua.LState, result tools.ToolResult) *lua.LTable {
	tbl := L.NewTable()
	L.SetField(tbl, "success", lua.LBool(result.Success))
	L.SetField(tbl, "content", lua.LString(result.Content))
	L.SetField(tbl, "error", lua.LString(result.Error))
	return tbl
}

// sessionOptions resolves the model, MaxIterations, and (when named) full
// agent definition a new/resumed session should use from an optional opts
// table passed to tyci.new_session/tyci.resume_session. opts may set:
//
//   - agent (string): a named agent definition (./.tyci/agents/<name>.md,
//     global falling back to ~/.tyci/agents/<name>.md, resolved from
//     e.WorkDir) — its `max_iterations` and `model` frontmatter seed the
//     session (the same source per-agent config values come from everywhere
//     else in the app, see internal/agentdefs.Def), and def is returned so
//     the caller can also apply its `tools:` whitelist and system prompt
//     (sessionAwait does this — see agentdefs below for why that matters:
//     without it, a session opted into an agent's smaller tool set in name
//     only, while the model conversation still got the full, ungated
//     top-level schema).
//   - max_iterations (number): overrides whatever the agent (or the
//     default) would otherwise supply.
//
// model is the value already passed positionally to new_session/
// resume_session (empty string if none). It wins over an agent's frontmatter
// model unless model is empty.
func (e *Engine) sessionOptions(opts *lua.LTable, model string) (resolvedModel string, maxIterations int, def *agentdefs.Def) {
	resolvedModel = model
	maxIterations = defaultSessionMaxIterations

	if opts == nil {
		return resolvedModel, maxIterations, nil
	}

	if agentName, ok := opts.RawGetString("agent").(lua.LString); ok && string(agentName) != "" {
		if found, ok := agentdefs.Get(e.WorkDir, string(agentName)); ok {
			def = &found
			if def.MaxIterations > 0 {
				maxIterations = def.MaxIterations
			}
			if resolvedModel == "" && def.Model != "" {
				resolvedModel = def.Model
			}
		}
	}

	// Only a positive value overrides: 0 (Lua's "false"-ish default for an
	// absent field coerced by a careless caller) or a negative number must
	// not silently mean "unlimited" — same guard as def.MaxIterations > 0
	// above.
	if mi, ok := opts.RawGetString("max_iterations").(lua.LNumber); ok && mi > 0 {
		maxIterations = int(mi)
	}

	return resolvedModel, maxIterations, def
}

// luaNewSession creates a new agent session. The optional second argument is
// an opts table — see sessionOptions — that can name an agent definition
// (applying its tools: whitelist, system prompt, and max_iterations) and/or
// set max_iterations directly.
func (e *Engine) luaNewSession(L *lua.LState) int {
	model := L.OptString(1, "")
	opts := L.OptTable(2, nil)
	model, maxIterations, def := e.sessionOptions(opts, model)

	session := &luaSession{
		engine:        e,
		model:         model,
		maxIterations: maxIterations,
		agentDef:      def,
		messages:      []providers.RichMessage{},
	}

	// Store session in registry
	sessionKey := fmt.Sprintf("session_%d", len(e.sessions))
	e.sessions[sessionKey] = session

	// Create metatable for session methods
	mt := L.NewTable()
	L.SetField(mt, "__index", e.newSessionMethods())

	tbl := L.NewTable()
	L.SetField(tbl, "model", lua.LString(model))
	L.SetField(tbl, "messages", L.NewTable())
	L.SetField(tbl, "_session_key", lua.LString(sessionKey))

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
		Model    string                  `json:"model"`
		Messages []providers.RichMessage `json:"messages"`
	}
	if err := json.Unmarshal(data, &sessionData); err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}

	opts := L.OptTable(2, nil)
	model, maxIterations, def := e.sessionOptions(opts, sessionData.Model)

	session := &luaSession{
		engine:        e,
		model:         model,
		maxIterations: maxIterations,
		agentDef:      def,
		messages:      sessionData.Messages,
	}

	// Store session in registry
	sessionKey := fmt.Sprintf("session_%d", len(e.sessions))
	e.sessions[sessionKey] = session

	// Create metatable
	mt := L.NewTable()
	L.SetField(mt, "__index", e.newSessionMethods())

	tbl := L.NewTable()
	L.SetField(tbl, "model", lua.LString(session.model))
	L.SetField(tbl, "messages", L.NewTable())
	L.SetField(tbl, "_session_key", lua.LString(sessionKey))

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
	session := checkSession(L, e)
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
	session := checkSession(L, e)
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
	session := checkSession(L, e)

	// Get provider and model
	provider, modelName, ok := providers.FindModel(session.model)
	if !ok {
		L.Push(lua.LNil)
		L.Push(lua.LString("model not found: " + session.model))
		return 2
	}

	// Create collector
	collector := &responseCollector{}

	// Build config. Tools/Schema wire the same global tool registry a normal
	// top-level session gets (see main.go's toolsAdapter +
	// tools.GetTopLevelToolsSchemaJSON) — without this, a session the engine
	// drives can be told to call a tool but the model never receives a tool
	// schema, and any call it emits anyway has nothing to dispatch it.
	// MaxIterations comes from the session (see sessionOptions): script- or
	// agent-frontmatter-settable instead of a hardcoded constant.
	//
	// When the session was created with a named agent (opts.agent), that
	// agent's tools: whitelist, system prompt and recursion restriction are
	// applied the same way main.go's real subagent path
	// (subagentToolRunner.Run + agentRunner.RunTaskWithSystem) applies a
	// named agent's definition — not just MaxIterations/Model. Without this
	// the session would be opted into a smaller tool set in name only: the
	// model conversation would still see the full, ungated top-level
	// schema, exactly the gap a named agent's tools: list exists to close.
	systemPrompt := providers.BuildSystemPrompt()
	schema := tools.GetTopLevelToolsSchemaJSON()
	runCtx := e.ctx

	if def := session.agentDef; def != nil {
		if def.SystemPromptMode == agentdefs.SystemPromptModeReplace {
			systemPrompt = def.SystemPrompt
		} else {
			// hasAskParent is always false here, unlike the real subagent
			// path (main.go's agentRunner.RunTaskWithSystem), which derives
			// it from the agent's tools: whitelist: a workflow session has
			// no job id (it isn't spawned via the subagent/job machinery),
			// so ask_parent always fails with "only works inside a job"
			// regardless of whether it's on the whitelist. Claiming it's a
			// real, usable tool here would be a false promise the model
			// only discovers by calling it and getting an error — see
			// buildSubagentContractNote/F22 for why that distinction
			// matters.
			systemPrompt = providers.BuildSubagentSystemPromptWithRole(def.SystemPrompt, false)
		}
		// A named-agent workflow session is, for item 21's depth gate,
		// exactly the same shape as an ordinary restricted subagent: it
		// runs through engineToolRunner -> tools.RunTool with
		// DenySubagentRecursion/AllowOnlySubagent gates, same as
		// subagentToolRunner.Run's child. Its depth must say so too —
		// otherwise runCtx defaults to depth 0, which offers "scout" in an
		// UNRESTRICTED definition's schema (GetSubagentToolsSchema includes
		// it unconditionally — it is not one of subagentDeniedTools) while
		// the depth-0 runtime gate in tools.RunTool refuses it. sessionDepth
		// is the session's own caller depth (e.ctx's, normally 0) plus one,
		// the same "caller depth + 1" rule runSingleTask applies to every
		// other child (tools/subagent.go). The PLAIN workflow session above
		// (def == nil) deliberately does NOT get this: it is meant to
		// behave exactly like the real top-level conversation (see this
		// func's own comment above schema's declaration), so it stays at
		// depth 0 and keeps "subagent".
		sessionDepth := tools.DepthFromContext(e.ctx) + 1
		runCtx = tools.WithDepth(runCtx, sessionDepth)
		// This named-agent session reaches scout-eligible depth (>=1)
		// without ever going through runSingleTask, which is otherwise the
		// only stamper of scout's per-caller concurrency bucket (see
		// WithScoutCaller's doc comment in tools/scout.go). Unlike btw.go's
		// two sites, a workflow session has no job id to reuse (it isn't
		// spawned via the subagent/job machinery at all — see the
		// hasAskParent comment above), so mint a fresh one instead. This
		// runs once per sessionAwait call, mirroring runSingleTask minting a
		// fresh todoAgentID once per child invocation rather than once per
		// session's whole lifetime.
		runCtx = tools.WithScoutCaller(runCtx, tools.NewScoutCallerID("workflow-session"))
		if len(def.Tools) == 0 {
			// Unrestricted named agent: the depth-aware builder adds
			// "scout" back in at depth 1-3 — safe here because
			// AllowOnlySubagent(nil) below is a no-op (see its own call),
			// so nothing downstream denies "scout" once the depth check
			// two lines down (in tools.RunTool) has approved it.
			schema = tools.GetSubagentToolsSchemaJSONForAtDepth(def.Tools, sessionDepth)
		} else {
			// A RESTRICTED whitelist is different: AllowOnlySubagent(def.Tools)
			// below is one static gate wrapped onto runCtx for the whole
			// session, with no per-call exemption for "scout" the way
			// main.go's subagentToolRunner.Run has (see its isDelegationTool
			// switch) — so offering "scout" here would advertise a tool
			// that gate then refuses on every actual call. Keep the plain,
			// non-depth-aware schema (never offers "scout" unless def.Tools
			// explicitly lists it, which AllowOnlySubagent would then also
			// permit) until engineToolRunner grows that same per-call
			// exemption.
			schema = tools.GetSubagentToolsSchemaJSONFor(def.Tools)
		}
		// Deny "subagent"/"agents" recursion unconditionally (mirroring
		// main.go's subagentToolRunner.Run), then layer the agent's own
		// tools: whitelist gate on top when it has one. AllowOnlySubagent
		// returns nil for an unrestricted definition (Tools == nil), so
		// WithToolGate's nil check is a no-op in that case — the recursion
		// denial still applies.
		runCtx = tools.WithToolGate(runCtx, tools.DenySubagentRecursion())
		runCtx = tools.WithToolGate(runCtx, tools.AllowOnlySubagent(def.Tools))
	}

	cfg := agent.Config{
		System:        systemPrompt,
		MaxRetries:    1,
		MaxIterations: session.maxIterations,
		Tools:         engineToolRunner{},
		Schema:        schema,
	}

	// Run agent. Wrapped in ledger.Watch (F32) the same way every other
	// agent.Run call site delegating work is (main.go's runSingleTask,
	// fork.go, btw.go, conductor.go) — otherwise a workflow session's
	// tokens/dollars are recorded nowhere, invisible to ledger.Get(). This
	// wrap sits OUTSIDE the "if def := session.agentDef; def != nil" block
	// above, so it covers every session this engine drives, not only a
	// named-agent one — a PLAIN session (def == nil, the common case) is
	// recorded exactly the same way.
	//
	// ledger.Subagent for every workflow session, plain ones included: a
	// script-driven session is delegated work by construction — it is the
	// SCRIPT deciding to spend tokens, not a person typing into a
	// conversation — and this process has no "main conversation" for
	// ledger.Main to mean anything (there is no top-level agent.Run here
	// the way main.go has one). jobID "" because a workflow session has no
	// job id of its own — it isn't spawned via the subagent/job machinery
	// at all (see sessionDepth's comment above on how it still gets a depth
	// without one), the same already-documented "untracked job" case
	// main.go uses for a scout (main.go:552-566).
	client := provider.Client(modelName)
	_, err := agent.Run(runCtx, client, ledger.Watch(collector, ledger.Subagent, client.Provider(), client.Model(), ""), &session.messages, cfg)
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
	L.SetField(usage, "input", lua.LNumber(collector.usage.Input))
	L.SetField(usage, "output", lua.LNumber(collector.usage.Output))
	L.SetField(reply, "usage", usage)

	L.Push(reply)
	return 1
}

// sessionSave saves the session to a file.
func (e *Engine) sessionSave(L *lua.LState) int {
	session := checkSession(L, e)
	path := L.CheckString(2)

	sessionData := struct {
		Model    string                  `json:"model"`
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
	session := checkSession(L, e)

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
func checkSession(L *lua.LState, e *Engine) *luaSession {
	tbl := L.CheckTable(1)
	key := L.GetField(tbl, "_session_key").String()
	if key != "" {
		if session, ok := e.sessions[key]; ok {
			return session
		}
	}
	return &luaSession{
		engine:        e,
		model:         L.GetField(tbl, "model").String(),
		maxIterations: defaultSessionMaxIterations,
		messages:      []providers.RichMessage{},
	}
}

// luaSession represents a Lua-accessible agent session.
type luaSession struct {
	engine   *Engine
	model    string
	messages []providers.RichMessage
	// maxIterations is the tool-call iteration cap sessionAwait passes to
	// agent.Run — see sessionOptions for how it is resolved from
	// tyci.new_session/resume_session's opts table.
	maxIterations int
	// agentDef, when non-nil, is the named agent definition opts.agent
	// resolved to sessionOptions — sessionAwait applies its tools:
	// whitelist (tool gate + filtered schema) and system prompt, not just
	// MaxIterations/Model. nil means "no named agent": the session keeps
	// the unrestricted top-level tool set (today's default behavior).
	agentDef *agentdefs.Def
}

// engineToolRunner adapts the global tools registry to agent.ToolRunner —
// the same shape main.go's toolsAdapter uses for a normal top-level session
// — so a session the engine creates can actually call tyci tools from
// within the model conversation it drives.
type engineToolRunner struct{}

func (engineToolRunner) Run(ctx context.Context, name string, args map[string]any) (string, error) {
	result := tools.RunTool(ctx, name, args)
	if result.Success {
		// Surface result.Truncated the same way main.go's toolsAdapter does
		// — a stable, parseable suffix marker — so a workflow-driven
		// session sees the same "may be incomplete" signal a normal
		// top-level session does, instead of it being silently dropped.
		if result.Truncated {
			return result.Content + "\n\n" + tools.TruncatedMarker, nil
		}
		return result.Content, nil
	}
	return "", fmt.Errorf("%s", result.Error)
}

// responseCollector collects agent responses.
type responseCollector struct {
	content   string
	thinking  string
	toolCalls int
	usage     stream.Usage
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

func (c *responseCollector) Request(content string)          {}
func (c *responseCollector) ToolCallDelta(delta string)      {}
func (c *responseCollector) ToolCallEnd(name, result string) {}
func (c *responseCollector) ToolFinish()                     {}
func (c *responseCollector) ToolBlock(msg string)            {}

func (c *responseCollector) Summary(usage stream.Usage, stats stream.Stats) {
	c.usage = usage
}

func (c *responseCollector) Total(usage stream.Usage) {}

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
		return convertLuaTable(val)
	default:
		return val.String()
	}
}

// convertLuaTable converts a Lua table to either a Go slice or a Go map,
// depending on its shape — mirroring tools/lua_tool.go's convertLuaTable
// (the "lua" tool's own conversion), which the fix here is deliberately
// kept in lockstep with. A table is treated as an array when it has at
// least one entry and its keys are exactly 1..n, the same rule Lua's own
// ipairs/table.insert work by.
//
// Before this fix, tyci.subagent({tasks = {...}}) was dead: a Lua array
// passed through the old convertLuaValueToGo (which only kept string keys)
// arrived at tools/subagent.go as map[string]any{} instead of []any{...},
// and parseTasks rejects that with "tasks must be an array" — the fan-out
// sugar item 7 asked for never actually worked.
//
// An empty table is ambiguous (equally a list of nothing and a record with
// no fields) and becomes an empty map, matching what a JSON encoder does
// with it and what the tools on the receiving end expect (objects).
func convertLuaTable(t *lua.LTable) any {
	n := t.Len() // Lua's array length: the n of a 1..n run
	if n > 0 {
		// Confirm there are no string keys hiding alongside the array part;
		// a mixed table is a record that happens to have numbered fields,
		// and turning it into a list would drop them.
		mixed := false
		t.ForEach(func(key, _ lua.LValue) {
			if _, ok := key.(lua.LString); ok {
				mixed = true
			}
		})
		if !mixed {
			arr := make([]any, 0, n)
			for i := 1; i <= n; i++ {
				arr = append(arr, convertLuaValueToGo(t.RawGetInt(i)))
			}
			return arr
		}
	}

	result := make(map[string]any)
	t.ForEach(func(key, value lua.LValue) {
		if keyStr, ok := key.(lua.LString); ok {
			result[string(keyStr)] = convertLuaValueToGo(value)
		}
	})
	return result
}

// RunWorkflow executes a Lua workflow script.
func RunWorkflow(ctx context.Context, scriptPath, prompt string) (string, error) {
	engine := NewEngine(ctx, prompt)
	return engine.Run(scriptPath)
}
