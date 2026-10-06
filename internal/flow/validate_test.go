package flow

import (
	"strings"
	"testing"
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
