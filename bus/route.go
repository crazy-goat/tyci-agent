package bus

import "slices"

// maxDepth limits the walk up the tree. A cycle in the host's tree ends
// there.
const maxDepth = 64

// Tree gives the bus the agent hierarchy. The host sets it with WithTree.
// The bus calls the functions while it holds its lock. They must not call
// the bus, and they must not take a lock that a caller of Publish can hold.
//
// A nil ParentOf means that no agent has a known parent. A subtree broadcast
// to an agent then reaches nobody, and a whole-tree broadcast still reaches
// every agent subscription. A nil IsLive means that every agent is live.
type Tree struct {
	// ParentOf returns the parent of an agent. An empty parent means the
	// orchestrator. ok is false for an agent that the tree does not know.
	ParentOf func(agentID string) (parentID string, ok bool)
	// IsLive reports whether an agent can still receive messages.
	IsLive func(agentID string) bool
}

// mayBroadcast reports whether the sender from may broadcast to the subtree
// addressed by to. The orchestrator may broadcast to any subtree. An agent
// may broadcast only to its own subtree. Users and TUIs may not broadcast.
// The empty ID of a user is not an agent ID, so it never matches here.
func mayBroadcast(from, to Addr) bool {
	switch from.Type {
	case AddrOrchestrator:
		return true
	case AddrAgent:
		return from.ID != "" && from.ID == to.ID
	}
	return false
}

// isLive reports whether the host considers agent id live.
func (b *Bus) isLive(id string) bool {
	return b.tree.IsLive == nil || b.tree.IsLive(id)
}

// recipients returns the subscriptions that receive m. A subtree broadcast
// reaches the agent subscriptions below the broadcast root, never the
// sender.
func (b *Bus) recipients(subs []*Sub, m Message) []*Sub {
	var out []*Sub
	for _, s := range subs {
		if !s.filter.wants(m.Kind) {
			continue
		}
		if m.To.Type == AddrSubtree {
			if s.filter.To.Type == AddrAgent && s.filter.To != m.From && b.inSubtree(s.filter.To.ID, m.To.ID) {
				out = append(out, s)
			}
			continue
		}
		if s.filter.To == m.To {
			out = append(out, s)
		}
	}
	return out
}

// inSubtree reports whether agent id lies strictly below root. The root
// itself is not in its own subtree. An empty root means the whole tree.
func (b *Bus) inSubtree(id, root string) bool {
	if root == "" {
		return true
	}
	if id == root || b.tree.ParentOf == nil {
		return false
	}
	cur := id
	for range maxDepth {
		parent, ok := b.tree.ParentOf(cur)
		if !ok || parent == "" {
			return false
		}
		if parent == root {
			return true
		}
		cur = parent
	}
	return false
}

// wants reports whether the filter selects messages of kind.
func (f Filter) wants(kind Kind) bool {
	return len(f.Kinds) == 0 || slices.Contains(f.Kinds, kind)
}
