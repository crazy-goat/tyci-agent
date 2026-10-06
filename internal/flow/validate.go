package flow

import "fmt"

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
	for name, s := range wf.States {
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
		for key, target := range s.On {
			if _, ok := wf.States[target]; !ok {
				errs = append(errs, fmt.Errorf("state %q on %q targets unknown state %q", name, key, target))
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
