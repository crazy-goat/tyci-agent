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

// maxCallJobBindings bounds callJobs. The oldest binding is dropped first
// (FIFO), so a long session cannot grow the map without limit. A dropped call
// id is no longer an alias: wait and kill_job then treat it as a plain id.
const maxCallJobBindings = 4096

var callJobs = struct {
	mu    sync.Mutex
	m     map[callJobKey]string
	order []callJobKey // insertion order of the keys in m
}{m: make(map[callJobKey]string)}

// BindCallJob records that jobID was started by the tool call named in ctx. It
// does nothing when ctx has no call id. A later call with the same id in the
// same owner replaces the job id, which is how a resumed session reuses ids.
// At most maxCallJobBindings entries are kept; the oldest one is dropped first.
func BindCallJob(ctx context.Context, jobID string) {
	call, _ := ctx.Value(CallIDCtxKey{}).(string)
	if call == "" || jobID == "" {
		return
	}
	owner, _ := ctx.Value(JobIDCtxKey{}).(string)
	key := callJobKey{owner: owner, call: call}
	callJobs.mu.Lock()
	defer callJobs.mu.Unlock()
	if _, ok := callJobs.m[key]; !ok {
		callJobs.order = append(callJobs.order, key)
	}
	callJobs.m[key] = jobID
	for len(callJobs.order) > maxCallJobBindings {
		delete(callJobs.m, callJobs.order[0])
		callJobs.order = callJobs.order[1:]
	}
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
