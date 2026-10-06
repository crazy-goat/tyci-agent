package flow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
)

func TestParse_UnknownFieldIsError(t *testing.T) {
	data := []byte(`{"name":"demo","start":"a","states":{"a":{"check":"x.sh","bogus":1,"on":{"go":"end"}},"end":{"end":true}}}`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected error for unknown field, got nil")
	} else if !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("error should mention the unknown field, got %v", err)
	}
}

func TestValidateStructure_MissingStart(t *testing.T) {
	wf := &Workflow{Name: "demo", States: map[string]State{"end": {End: true}}}
	errs := validateStructure(wf)
	if len(errs) == 0 {
		t.Fatal("expected error for missing start, got none")
	}
}

func TestValidateStructure_UnknownTarget(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "a", States: map[string]State{
		"a":   {Check: "x.sh", On: map[string]string{"go": "missing"}},
		"end": {End: true},
	}}
	errs := validateStructure(wf)
	found := false
	for _, err := range errs {
		if strings.Contains(err.Error(), "missing") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected unknown-target error, got %v", errs)
	}
}

func TestValidateStructure_TwoKindsInOneState(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "a", States: map[string]State{
		"a":   {Check: "x.sh", Agent: "worker", On: map[string]string{"done": "end"}},
		"end": {End: true},
	}}
	errs := validateStructure(wf)
	found := false
	for _, err := range errs {
		if strings.Contains(err.Error(), `"a"`) && strings.Contains(err.Error(), "exactly one") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected two-kinds error for state a, got %v", errs)
	}
}

func TestValidateStructure_NoEndState(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "a", States: map[string]State{
		"a": {Check: "x.sh", On: map[string]string{"go": "a"}},
	}}
	errs := validateStructure(wf)
	found := false
	for _, err := range errs {
		if strings.Contains(err.Error(), "no end state") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected no-end-state error, got %v", errs)
	}
}

func TestValidateStructure_ReportsAllErrors(t *testing.T) {
	wf := &Workflow{
		Name:  "",
		Start: "ghost",
		States: map[string]State{
			"a": {Check: "x.sh", Agent: "worker", On: map[string]string{"go": "nowhere"}},
		},
	}
	errs := validateStructure(wf)
	if len(errs) < 4 {
		t.Fatalf("expected all errors (name, start, kinds, target, no-end), got %d: %v", len(errs), errs)
	}
}

func TestValidate_MaxVisitsNeedsAskState(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "a", States: map[string]State{
		"a":   {Check: "x.sh", MaxVisits: 2, On: map[string]string{"go": "end"}},
		"end": {End: true},
	}}
	errs := validateStructure(wf)
	found := false
	for _, err := range errs {
		if strings.Contains(err.Error(), `"ask"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected ask-state error, got %v", errs)
	}
	wf.States["ask"] = State{Ask: "help", On: map[string]string{"stop": "end"}}
	if errs := validateStructure(wf); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
}

func vCfg(models map[string]string, roles map[string]flowconfig.Role) *flowconfig.Config {
	return &flowconfig.Config{Models: models, DefaultModel: "m", Roles: roles}
}

func okResolve(rel string) (string, error) { return rel, nil }

func agentWF(role string) *Workflow {
	return &Workflow{Name: "demo", Start: "code", States: map[string]State{
		"code": {Agent: role, On: map[string]string{"done": "end"}},
		"end":  {End: true},
	}}
}

func TestValidate_UndefinedRole(t *testing.T) {
	cfg := vCfg(map[string]string{"m": "p/m"}, nil)
	_, err := Validate(agentWF("planner"), cfg, okResolve)
	if err == nil || !strings.Contains(err.Error(), `state "code"`) || !strings.Contains(err.Error(), `role "planner" is not defined`) {
		t.Fatalf("got %v", err)
	}
}

func TestValidate_RoleWithUnknownModel(t *testing.T) {
	cfg := vCfg(map[string]string{"m": "p/m"}, map[string]flowconfig.Role{"worker": {Model: "x", Prompt: "p"}})
	_, err := Validate(agentWF("worker"), cfg, okResolve)
	if err == nil || !strings.Contains(err.Error(), `role "worker"`) || !strings.Contains(err.Error(), `"x"`) {
		t.Fatalf("got %v", err)
	}
}

func TestValidate_UnknownTargetState(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "ci", States: map[string]State{
		"ci":  {Check: "c.sh", On: map[string]string{"red": "cod"}},
		"end": {End: true},
	}}
	_, err := Validate(wf, vCfg(nil, nil), okResolve)
	if err == nil || !strings.Contains(err.Error(), `state "ci": on "red" -> "cod" is not a state`) {
		t.Fatalf("got %v", err)
	}
}

func TestValidate_MissingCheckScript(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "ci", States: map[string]State{
		"ci":  {Check: "c.sh", On: map[string]string{"ok": "end"}},
		"end": {End: true},
	}}
	missing := filepath.Join(t.TempDir(), "c.sh")
	_, err := Validate(wf, vCfg(nil, nil), func(string) (string, error) { return missing, nil })
	if err == nil || !strings.Contains(err.Error(), `state "ci"`) || !strings.Contains(err.Error(), missing) {
		t.Fatalf("got %v", err)
	}
}

func TestValidate_SameModelWarnsOnly(t *testing.T) {
	cfg := vCfg(map[string]string{"m": "p/m"}, map[string]flowconfig.Role{"worker": {Prompt: "p"}, "review": {Prompt: "p"}})
	wf := &Workflow{Name: "demo", Start: "w", States: map[string]State{
		"w":   {Agent: "worker", On: map[string]string{"done": "r"}},
		"r":   {Agent: "review", On: map[string]string{"done": "end"}},
		"end": {End: true},
	}}
	warns, err := Validate(wf, cfg, okResolve)
	if err != nil || len(warns) != 1 {
		t.Fatalf("err=%v warns=%v", err, warns)
	}
}

func TestValidate_AllProblemsListedTogether(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "a", States: map[string]State{
		"a":   {Agent: "planner", On: map[string]string{"x": "nowhere"}},
		"b":   {Agent: "other", On: map[string]string{"x": "end"}},
		"end": {End: true},
	}}
	_, err := Validate(wf, vCfg(map[string]string{"m": "p/m"}, nil), okResolve)
	if err == nil || len(strings.Split(err.Error(), "\n")) != 3 {
		t.Fatalf("got %v", err)
	}
}

func TestValidate_UnreachableStateWarns(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "a", States: map[string]State{
		"a":    {Check: "c.sh", On: map[string]string{"ok": "end"}},
		"lost": {Check: "c.sh", On: map[string]string{"ok": "end"}},
		"end":  {End: true},
	}}
	f := filepath.Join(t.TempDir(), "c.sh")
	if err := os.WriteFile(f, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	warns, err := Validate(wf, vCfg(nil, nil), func(string) (string, error) { return f, nil })
	if err != nil || len(warns) != 1 || !strings.Contains(warns[0], `"lost"`) {
		t.Fatalf("err=%v warns=%v", err, warns)
	}
}
