package tools

import (
	"sync"

	"github.com/crazy-goat/tyci-agent/jobs"
)

// testMailbox is an in-memory JobMailbox for tests. Liveness comes from reg,
// the same way main's jobMailboxAdapter (btw.go) reads it from the registry.
type testMailbox struct {
	reg    *jobs.Registry
	mu     sync.Mutex
	queue  map[string][]string
	posted map[string]uint64
}

func newTestMailbox(reg *jobs.Registry) *testMailbox {
	return &testMailbox{reg: reg, queue: map[string][]string{}, posted: map[string]uint64{}}
}

func (m *testMailbox) Resolve(id string) (string, bool) { return m.reg.Resolve(id) }

func (m *testMailbox) IsLive(id string) bool { return m.reg.IsLive(id) }

func (m *testMailbox) Post(id, text string) bool {
	if !m.reg.IsLive(id) {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queue[id] = append(m.queue[id], text)
	m.posted[id]++
	return true
}

func (m *testMailbox) Drain(id string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.queue[id]
	delete(m.queue, id)
	return out
}

func (m *testMailbox) Posted(id string) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.posted[id]
}
