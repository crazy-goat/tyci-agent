package tools

import (
	"sync"
)

// multiWaiter answers per job id, so one test can run jobs in different states.
type multiWaiter struct {
	mu   sync.Mutex
	jobs map[string]JobStatus
}

func (w *multiWaiter) set(id string, s JobStatus) {
	w.mu.Lock()
	w.jobs[id] = s
	w.mu.Unlock()
}
