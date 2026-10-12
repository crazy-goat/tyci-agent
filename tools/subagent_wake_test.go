package tools

// B5 (batch-2 audit): runWithHandoff's blocking-call select used to be
// blind to a spawned child entering StatusWaitingAnswer — it only noticed a
// blocked child indirectly, by sitting out the rest of SubagentBackgroundAfter
// (or the whole call, without handoff) before the handoff even let the
// parent see the queued ask-notice. These tests assert on the wake itself
// (a channel closing / a goroutine unblocking), not on wall-clock luck.
//
// Review round 1 (C1) found that the fix as first written broke worse than
// the bug: the watcher used jobs.Registry.Wait, which counts toward
// QuestionHasWaiter, so Ask suppressed the onEvent notice for essentially
// every blocking subagent call — and the watcher itself delivers the
// question nowhere. The fix is JobObserver (backed by
// jobs.Registry.WaitObserve), which does not count, PLUS handOff itself
// now asks the observer what each still-running child's pending question
// is (pendingQuestions) and puts it in the handoff message — so the
// message's own promise ("you will be told... when one is BLOCKED on a
// question") is something it can make good on directly, not only
// something the onEvent notice might.

// realJobObserver wraps a real jobs.Registry via WaitObserve — the
// non-counting path runWithHandoff's watcher and handOff's pendingQuestions
// peek actually use in production (see main.go's jobObserverAdapter). Using
// WaitObserve (not Wait) here is what makes these tests exercise the real
// fix for C1 rather than the pre-fix shape — see JobObserver's doc comment
// for the distinction.

// jobEventFunc adapts a plain function to jobs.EventPublisher for tests.
