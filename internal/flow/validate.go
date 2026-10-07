package flow

import (
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
)

// validateStructure checks the structural rules of section 5.1 that belong
// to this issue and returns ALL errors found:
//   - name, start and states are present and start exists,
//   - every on target exists,
//   - exactly one kind (check, agent, ask, end) per state,
//   - at least one end state.
//
// max_visits needs a state named ask of kind ask (rule 6).
// Unreachable-state warnings (SDR rule 7) and role/script resolution (rules 4, 5) belong to later issues and are
// deliberately not checked here; the seams (MaxVisits, Task, Ask fields) stay.
func validateStructure(wf *Workflow) []error {
	var errs []error
	if wf == nil {
		return []error{fmt.Errorf("workflow is nil")}
	}
	if wf.Name == "" {
		errs = append(errs, fmt.Errorf("workflow name is empty"))
	}
	if wf.Start == "" {
		errs = append(errs, fmt.Errorf("workflow start is empty"))
	}
	if len(wf.States) == 0 {
		errs = append(errs, fmt.Errorf("workflow states are empty"))
	}
	if wf.Start != "" && len(wf.States) > 0 {
		if _, ok := wf.States[wf.Start]; !ok {
			errs = append(errs, fmt.Errorf("start state %q does not exist", wf.Start))
		}
	}
	hasEnd := false
	for _, name := range sortedStates(wf) {
		s := wf.States[name]
		kinds := 0
		if s.Check != "" {
			kinds++
		}
		if s.Agent != "" {
			kinds++
		}
		if s.Ask != "" {
			kinds++
		}
		if s.End {
			kinds++
			hasEnd = true
		}
		if kinds != 1 {
			errs = append(errs, fmt.Errorf("state %q must have exactly one of check, agent, ask, end, got %d", name, kinds))
		}
		for _, key := range sortedKeys(s.On) {
			target := s.On[key]
			if _, ok := wf.States[target]; !ok && target != FailedTarget {
				errs = append(errs, fmt.Errorf("state %q: on %q -> %q is not a state", name, key, target))
			}
		}
	}
	if st, ok := wf.States["ask"]; (wf.Defaults.MaxVisits != 0 || anyMaxVisits(wf)) && (!ok || st.Ask == "") {
		errs = append(errs, fmt.Errorf(`max_visits is set, so the workflow needs a state named "ask" of kind ask`))
	}
	if len(wf.States) > 0 && !hasEnd {
		errs = append(errs, fmt.Errorf("workflow has no end state"))
	}
	return errs
}

func anyMaxVisits(wf *Workflow) bool {
	for _, s := range wf.States {
		if s.MaxVisits != 0 {
			return true
		}
	}
	return false
}

func sortedStates(wf *Workflow) []string {
	names := make([]string, 0, len(wf.States))
	for n := range wf.States {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Resolver maps a relative check path to an absolute script path.
type Resolver func(rel string) (abs string, err error)

// Validate returns warnings and one joined error with ALL problems found.
// It runs validateStructure first, then the rules that need the config and
// the file system (SDR 5.1 rules 4, 5, 7, 8).
func Validate(wf *Workflow, cfg *flowconfig.Config, resolveCheck Resolver) (warnings []string, err error) {
	errs := validateStructure(wf)
	if wf == nil {
		return nil, errors.Join(errs...)
	}
	models := map[string]string{}
	for _, name := range sortedStates(wf) {
		s := wf.States[name]
		if s.Agent != "" {
			role, rerr := cfg.Role(s.Agent)
			if rerr != nil {
				errs = append(errs, fmt.Errorf("state %q: %w", name, rerr))
			} else if uri, merr := cfg.ResolveModel(role); merr != nil {
				errs = append(errs, fmt.Errorf("state %q: role %q: %w", name, s.Agent, merr))
			} else {
				models[s.Agent] = uri
			}
		}
		if s.Check != "" {
			abs, cerr := resolveCheck(s.Check)
			if cerr != nil {
				errs = append(errs, fmt.Errorf("state %q: check %q: %w", name, s.Check, cerr))
			} else if st, serr := os.Stat(abs); serr != nil || st.IsDir() {
				errs = append(errs, fmt.Errorf("state %q: check script %q does not exist", name, abs))
			}
		}
	}
	if w, ok := models["worker"]; ok && w == models["review"] {
		warnings = append(warnings, "roles worker and review use the same model")
	}
	reach := reachable(wf)
	for _, name := range sortedStates(wf) {
		if !reach[name] {
			warnings = append(warnings, fmt.Sprintf("state %q is unreachable from start", name))
		}
	}
	return warnings, errors.Join(errs...)
}

// reachable walks from start over on targets. When any max_visits is set,
// the implicit "ask" target counts as reachable.
func reachable(wf *Workflow) map[string]bool {
	seen := map[string]bool{}
	stack := []string{wf.Start}
	if wf.Defaults.MaxVisits != 0 || anyMaxVisits(wf) {
		stack = append(stack, "ask")
	}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		s, ok := wf.States[n]
		if !ok || seen[n] {
			continue
		}
		seen[n] = true
		for _, t := range s.On {
			stack = append(stack, t)
		}
	}
	return seen
}
