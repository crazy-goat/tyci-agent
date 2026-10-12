package tools

import (
	"context"
	"sync"
)

// CallIDCtxKey carries the call id of a tool call in its context. The agent
// dispatcher sets it: "c1", "c2", and so on. A backgrounded call shows that id
// to the model, and wait and kill_job accept it as an alias of the job id.
type CallIDCtxKey struct{}

// callJobKey names a call. The owner is the job id of the agent that made the
// call (JobIDCtxKey), empty for the main conversation. Call ids are only unique
// per agent, so the owner is part of the key.
type callJobKey struct {
	owner string
	call  string
}

var callJobs = struct {
	mu sync.Mutex
	m  map[callJobKey]string
}{m: make(map[callJobKey]string)}

// BindCallJob records that jobID was started by the tool call named in ctx. It
// does nothing when ctx has no call id. The entry is never removed: a later
// call with the same id in the same owner replaces it, which is how a resumed
// session reuses ids.
func BindCallJob(ctx context.Context, jobID string) {
	call, _ := ctx.Value(CallIDCtxKey{}).(string)
	if call == "" || jobID == "" {
		return
	}
	owner, _ := ctx.Value(JobIDCtxKey{}).(string)
	callJobs.mu.Lock()
	defer callJobs.mu.Unlock()
	callJobs.m[callJobKey{owner: owner, call: call}] = jobID
}

// resolveCallID returns the job id that the call id names for owner. An id
// that names no bound call is returned unchanged.
func resolveCallID(owner, id string) string {
	if id == "" {
		return id
	}
	callJobs.mu.Lock()
	defer callJobs.mu.Unlock()
	if job, ok := callJobs.m[callJobKey{owner: owner, call: id}]; ok {
		return job
	}
	return id
}
