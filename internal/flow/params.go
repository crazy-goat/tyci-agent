package flow

import (
	"fmt"
	"regexp"
	"strconv"
)

// IssueParam is the name of the param that holds the issue number of a run.
const IssueParam = "issue"

var paramName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// MissingParamError is returned by BindParams for a required param without a value.
// Callers find the param with errors.As.
type MissingParamError struct{ Name, Description string }

func (e MissingParamError) Error() string {
	return fmt.Sprintf("missing required param %s: %s", e.Name, e.Description)
}

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
			return nil, MissingParamError{Name: p.Name, Description: p.Description}
		}
		if p.Name == IssueParam && v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("param %q must be a positive number, got %q", p.Name, v)
			}
			v = strconv.Itoa(n) // canonical text, the same as {{.Issue}}
		}
		out[p.Name] = v
	}
	return out, nil
}

// IssueArgs returns the positional values that start wf for issue. The issue goes
// to the param named "issue"; every other param gets an empty value. It refuses a
// workflow that declares no issue param, so an issue never lands on another param.
func IssueArgs(wf *Workflow, issue int) ([]string, error) {
	const hint = `add {"name":"issue","description":"GitHub issue number","required":true} to workflow.json`
	if len(wf.Params) == 0 {
		return nil, fmt.Errorf("workflow %q declares no params; %s", wf.Name, hint)
	}
	args := make([]string, len(wf.Params))
	found := false
	for i, p := range wf.Params {
		if p.Name == IssueParam {
			args[i] = strconv.Itoa(issue)
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("workflow %q declares no %q param; %s", wf.Name, IssueParam, hint)
	}
	return args, nil
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
