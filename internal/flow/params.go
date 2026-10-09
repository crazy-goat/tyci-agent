package flow

import (
	"fmt"
	"regexp"
	"strconv"
)

// IssueParam is the name of the param that holds the issue number of a run.
const IssueParam = "issue"

var paramName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// BindParams gives the positional values of a run to the params of wf. Every
// declared param gets a key, so a template can read it; an optional param without
// a value is "". It returns one error for the first problem: extra values, a
// missing required value, or an issue that is not a positive number.
func BindParams(wf *Workflow, values []string) (map[string]string, error) {
	if len(values) > len(wf.Params) {
		return nil, fmt.Errorf("workflow %q takes %d param(s), got %d value(s)", wf.Name, len(wf.Params), len(values))
	}
	out := make(map[string]string, len(wf.Params))
	for i, p := range wf.Params {
		v := ""
		if i < len(values) {
			v = values[i]
		}
		if v == "" && p.Required {
			return nil, fmt.Errorf("workflow %q needs param %q: %s", wf.Name, p.Name, p.Description)
		}
		if p.Name == IssueParam && v != "" {
			if n, err := strconv.Atoi(v); err != nil || n <= 0 {
				return nil, fmt.Errorf("param %q must be a positive number, got %q", p.Name, v)
			}
		}
		out[p.Name] = v
	}
	return out, nil
}

// issueOf returns the issue number of bound params, or 0 when the workflow has
// no issue param or it is empty.
func issueOf(params map[string]string) int {
	n, _ := strconv.Atoi(params[IssueParam])
	return n
}

// validateParams returns one error for each invalid or duplicate param name of wf.
func validateParams(wf *Workflow) []error {
	var errs []error
	seen := map[string]bool{}
	for _, p := range wf.Params {
		switch {
		case !paramName.MatchString(p.Name):
			errs = append(errs, fmt.Errorf("param name %q must match ^[a-z][a-z0-9_]*$", p.Name))
		case seen[p.Name]:
			errs = append(errs, fmt.Errorf("param %q is declared twice", p.Name))
		}
		seen[p.Name] = true
	}
	return errs
}
