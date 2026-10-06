package flow

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	lua "github.com/yuin/gopher-lua"

	"github.com/crazy-goat/tyci-agent/tools"
)

// runLuaCheck runs a Lua check in a sandboxed state (no io, no os.execute).
// The env is exposed as the global table "env". A string return value is the
// key; a number is the exit code.
func runLuaCheck(ctx context.Context, script string, env []string) (string, CheckResult, error) {
	var res CheckResult
	L := lua.NewState()
	defer L.Close()
	tools.RestrictLuaStdlib(L)
	L.SetContext(ctx)
	t := L.NewTable()
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			t.RawSetString(k, lua.LString(v))
		}
	}
	L.SetGlobal("env", t)
	if err := L.DoFile(script); err != nil {
		if ctx.Err() != nil {
			return "timeout", res, nil
		}
		return "", res, fmt.Errorf("lua check: %w", err)
	}
	ret := L.Get(-1)
	switch v := ret.(type) {
	case lua.LString:
		return strings.TrimSpace(string(v)), res, nil
	case lua.LNumber:
		code := int(v)
		res.Exit = &code
		return strconv.Itoa(code), res, nil
	}
	return "", res, fmt.Errorf("lua check must return a string or a number, got %s", ret.Type())
}
