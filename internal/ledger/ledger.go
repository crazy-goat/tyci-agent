// Package ledger keeps one running total of what a session has spent.
//
// It exists because usage is accumulated in the wrong shape for the question a
// person actually asks. The Conductor totals the usage of its own turns, and a
// subagent reports its usage into the tool result it returns to the parent —
// so the one number nobody has is "what has this session cost me, children
// included". A process-wide ledger is the honest answer: every agent run,
// whether it is the conversation you are typing into or the fifth child of an
// async fan-out, records against the same total.
//
// Prices come from internal/pricing, which may not know a model. Unpriced
// tokens are counted separately rather than as zero dollars, because a bar
// that shows "$0.00" for real spending is worse than one that admits it does
// not know.
package ledger

import (
	"sync"

	"github.com/decodo/tyci/internal/pricing"
	"github.com/decodo/tyci/stream"
)

// Kind separates the conversation from work it delegated. That split is the
// interesting one: an unexpectedly large bill is nearly always children.
//
// Scout is its own Kind rather than folding into Subagent, even though a
// scout IS a (deliberately crippled) subagent under the hood — item 21
// round B needs to answer "how much of this did scouts cost" on its own,
// which a shared bucket with ordinary subagents could never show. Token
// rollups (Get's Main/Sub) still treat Scout as delegated work alongside
// Subagent (see Get below) — only the dollar figure gets its own line, since
// nothing here needs to tell a scout's tokens apart from a subagent's, only
// its cost.
type Kind int

const (
	Main Kind = iota
	Subagent
	Scout
)

func (k Kind) String() string {
	switch k {
	case Subagent:
		return "subagent"
	case Scout:
		return "scout"
	default:
		return "main"
	}
}

// Row is the accumulated usage of one model, for one Kind, for one job.
//
// JobID joins a subagent's own job id into the row's identity so that a
// child's tokens are separately trackable instead of every child on the same
// model collapsing into one shared "subagent" bucket (TODO item 1's
// Subagents tab needs per-child counts to build its tree). It is empty for
// the main conversation (which is not a job) and for any call made outside a
// tracked job — Get/Snapshot's rollups (MainUSD, SubagentUSD, ScoutUSD,
// Main, Sub) do not key off JobID at all, so they stay correct regardless.
type Row struct {
	Kind     Kind
	Provider string
	Model    string
	JobID    string
	Usage    stream.Usage
	// USD is the cost of Usage, or 0 when Priced is false.
	USD    float64
	Priced bool
	// Runs is how many agent runs contributed.
	Runs int
}

// Snapshot is a consistent view of the ledger, safe to hold and render.
type Snapshot struct {
	Rows []Row
	// MainUSD, SubagentUSD and ScoutUSD sum only the rows whose model was
	// priced.
	MainUSD     float64
	SubagentUSD float64
	ScoutUSD    float64
	// Unpriced is the number of rows whose model has no price in the catalog.
	// Non-zero means TotalUSD is a lower bound, not the bill.
	Unpriced int
	Main     stream.Usage
	Sub      stream.Usage
}

// TotalUSD is main plus everything delegated, scouts included.
func (s Snapshot) TotalUSD() float64 { return s.MainUSD + s.SubagentUSD + s.ScoutUSD }

type key struct {
	kind     Kind
	provider string
	model    string
	jobID    string
}

var (
	mu    sync.Mutex
	rows  = map[key]*Row{}
	order []key
)

// Record adds one agent run's usage, attributing it to jobID (empty for the
// main conversation, or for a call made outside a tracked job — see Row's
// doc comment). Calling it with an all-zero usage is a no-op: a turn that
// failed before the first token should not create a row.
func Record(kind Kind, provider, model, jobID string, u stream.Usage) {
	if u == (stream.Usage{}) {
		return
	}
	k := key{kind: kind, provider: provider, model: model, jobID: jobID}
	mu.Lock()
	defer mu.Unlock()
	r, ok := rows[k]
	if !ok {
		r = &Row{Kind: kind, Provider: provider, Model: model, JobID: jobID}
		rows[k] = r
		order = append(order, k)
	}
	r.Usage.Add(u)
	r.Runs++
	rates, _ := pricing.Lookup(provider, model)
	r.Priced = rates.Known()
	r.USD = Cost(rates, r.Usage)
}

// Cost prices a usage total. Input is treated as including CacheRead (which is
// how the status bar has always displayed it), so cached tokens are billed at
// the cache rate and only the remainder at the input rate. A provider that
// does not price caching separately falls back to the input rate, which is
// what "no cache discount" means.
func Cost(r pricing.Rates, u stream.Usage) float64 {
	if !r.Known() {
		return 0
	}
	fresh := u.Input - u.CacheRead
	if fresh < 0 {
		fresh = 0
	}
	cacheRead := r.CacheRead
	if cacheRead == 0 {
		cacheRead = r.Input
	}
	cacheWrite := r.CacheWrite
	if cacheWrite == 0 {
		cacheWrite = r.Input
	}
	const perMillion = 1_000_000.0
	return float64(fresh)/perMillion*r.Input +
		float64(u.CacheRead)/perMillion*cacheRead +
		float64(u.CacheWrite)/perMillion*cacheWrite +
		float64(u.Output)/perMillion*r.Output
}

// Get returns the current totals, in first-seen order.
func Get() Snapshot {
	mu.Lock()
	defer mu.Unlock()
	s := Snapshot{Rows: make([]Row, 0, len(order))}
	for _, k := range order {
		r := rows[k]
		s.Rows = append(s.Rows, *r)
		switch {
		case !r.Priced:
			s.Unpriced++
		case r.Kind == Subagent:
			s.SubagentUSD += r.USD
		case r.Kind == Scout:
			s.ScoutUSD += r.USD
		default:
			s.MainUSD += r.USD
		}
		if r.Kind == Subagent || r.Kind == Scout {
			s.Sub.Add(r.Usage)
		} else {
			s.Main.Add(r.Usage)
		}
	}
	return s
}

// ByModel aggregates Get()'s rows back down to one row per {Kind, Provider,
// Model} — the shape a per-model breakdown (the Tokens tab's session list)
// wants, regardless of how many separate jobs (the main conversation, or any
// number of subagents) contributed to it. Without this, joining a job id
// into Row's key (so the Subagents tab can track a child's tokens
// separately — see Row's doc comment) would otherwise leak into every OTHER
// consumer of Get().Rows too: twelve subagents on the same model would show
// as twelve identical-looking lines instead of one aggregated one. Returned
// rows carry no JobID (it no longer identifies a single job) and are
// ordered by first-seen model, mirroring Get()'s own ordering.
func ByModel() []Row {
	mu.Lock()
	defer mu.Unlock()
	type mkey struct {
		kind     Kind
		provider string
		model    string
	}
	idx := map[mkey]int{}
	var out []Row
	for _, k := range order {
		r := rows[k]
		mk := mkey{kind: k.kind, provider: k.provider, model: k.model}
		if i, ok := idx[mk]; ok {
			agg := &out[i]
			agg.Usage.Add(r.Usage)
			agg.USD += r.USD
			agg.Runs += r.Runs
			if !r.Priced {
				agg.Priced = false
			}
			continue
		}
		idx[mk] = len(out)
		out = append(out, Row{
			Kind: r.Kind, Provider: r.Provider, Model: r.Model,
			Usage: r.Usage, USD: r.USD, Priced: r.Priced, Runs: r.Runs,
		})
	}
	return out
}

// JobUsage is one job's own usage and cost, aggregated across every model it
// called — a job can fall back across models mid-run, and the Subagents tab
// wants one figure per row, not one per model.
type JobUsage struct {
	Usage stream.Usage
	// USD is priced usage only; see Priced.
	USD float64
	// Priced is false when any model this job used has no catalog price, so
	// USD understates the true cost — callers may surface that how they
	// like; the TUI currently renders the plain USD figure either way.
	Priced bool
}

// UsageByJob returns each tracked job's own usage and cost, keyed by job id.
// Rows with an empty JobID (the main conversation, or a call made outside a
// tracked job) are excluded — this exists for the Subagents tab, which has
// no use for either.
//
// Deliberately per-job, not rolled up across a parent-child tree: tokens are
// not additive across models in any useful sense (see TODO.md item 1), so
// only the caller — walking jobs.Job.ParentID — decides whether and how to
// sum cost across a subtree.
func UsageByJob() map[string]JobUsage {
	mu.Lock()
	defer mu.Unlock()
	out := map[string]JobUsage{}
	priced := map[string]bool{}
	for _, k := range order {
		if k.jobID == "" {
			continue
		}
		r := rows[k]
		agg := out[k.jobID]
		agg.Usage.Add(r.Usage)
		agg.USD += r.USD
		out[k.jobID] = agg
		if _, seen := priced[k.jobID]; !seen {
			priced[k.jobID] = true
		}
		if !r.Priced {
			priced[k.jobID] = false
		}
	}
	for id, p := range priced {
		agg := out[id]
		agg.Priced = p
		out[id] = agg
	}
	return out
}

// Reset clears the ledger. /new starts a new conversation, and carrying the
// previous one's bill into it would misreport both.
func Reset() {
	mu.Lock()
	rows, order = map[key]*Row{}, nil
	mu.Unlock()
}
