package bus

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"
)

const (
	kindDurable     Kind = "test.durable"
	kindOther       Kind = "test.other"
	kindLatest      Kind = "test.latest"
	kindLatestOther Kind = "test.latest-other"
	kindChan        Kind = "test.chan"
)

var orchestrator = Addr{Type: AddrOrchestrator}

type item struct {
	ID string `json:"id"`
	N  int    `json:"n"`
}

type withChan struct {
	C chan int `json:"c"`
}

func itemKey(v item) string { return v.ID }

func init() {
	Register[item](kindDurable, Durable, nil)
	Register[item](kindOther, Durable, nil)
	Register[item](kindLatest, Latest, itemKey)
	Register[item](kindLatestOther, Latest, itemKey)
	Register[withChan](kindChan, Durable, nil)
}

func agent(id string) Addr { return Addr{Type: AddrAgent, ID: id} }

func subtree(id string) Addr { return Addr{Type: AddrSubtree, ID: id} }

// mustPublish publishes p from the orchestrator to the agent id and returns the Seq.
func mustPublish(t *testing.T, b *Bus, kind Kind, p item) uint64 {
	t.Helper()
	seq, err := Publish(b, kind, orchestrator, agent("x"), OriginSystem, p)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	return seq
}

// decodeItems decodes the payloads of msgs as item values.
func decodeItems(t *testing.T, msgs []Message) []item {
	t.Helper()
	out := make([]item, 0, len(msgs))
	for _, m := range msgs {
		v, err := Decode[item](m)
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		out = append(out, v)
	}
	return out
}

func TestPublish_UnregisteredKind_ReturnsError(t *testing.T) {
	b := New()
	_, err := Publish(b, "test.missing", orchestrator, agent("x"), OriginSystem, item{})
	if err == nil {
		t.Fatal("Publish of an unregistered kind returned no error")
	}
	if seq := mustPublish(t, b, kindDurable, item{ID: "a"}); seq != 1 {
		t.Fatalf("Seq after a failed Publish = %d, want 1", seq)
	}
}

func TestPublish_UnencodablePayload_ReturnsError(t *testing.T) {
	b := New()
	_, err := Publish(b, kindChan, orchestrator, agent("x"), OriginSystem, withChan{C: make(chan int)})
	if err == nil {
		t.Fatal("Publish of an unencodable payload returned no error")
	}
	if seq := mustPublish(t, b, kindDurable, item{ID: "a"}); seq != 1 {
		t.Fatalf("Seq after a failed Publish = %d, want 1", seq)
	}
}

func TestPublish_PayloadTypeMismatch_ReturnsError(t *testing.T) {
	b := New()
	_, err := Publish(b, kindDurable, orchestrator, agent("x"), OriginSystem, withChan{})
	if err == nil {
		t.Fatal("Publish with another payload type returned no error")
	}
}

func TestPublish_AfterClose_ReturnsErrClosed(t *testing.T) {
	b := New()
	b.Close()
	_, err := Publish(b, kindDurable, orchestrator, agent("x"), OriginSystem, item{})
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("Publish after Close: err = %v, want ErrClosed", err)
	}
}

func TestBusClose_Idempotent(t *testing.T) {
	b := New()
	b.Close()
	b.Close()
}

func TestDrain_WorksAfterBusClose(t *testing.T) {
	b := New()
	s := b.Subscribe("x", Filter{To: agent("x")})
	mustPublish(t, b, kindDurable, item{ID: "a", N: 1})
	b.Close()

	got := decodeItems(t, s.Drain())
	if !slices.Equal(got, []item{{ID: "a", N: 1}}) {
		t.Fatalf("Drain after Close = %+v, want the queued message", got)
	}
}

func TestSubClose_StopsReceiving_QueueDiscarded(t *testing.T) {
	b := New()
	s := b.Subscribe("x", Filter{To: agent("x")})
	mustPublish(t, b, kindDurable, item{ID: "a"})
	mustPublish(t, b, kindLatest, item{ID: "k"})

	s.Close()
	s.Close()
	mustPublish(t, b, kindDurable, item{ID: "b"})

	if got := s.Drain(); got != nil {
		t.Fatalf("Drain of a closed Sub = %+v, want nothing", got)
	}
}

func TestSubClose_ClearsPendingReady(t *testing.T) {
	b := New()
	s := b.Subscribe("x", Filter{To: agent("x")})
	mustPublish(t, b, kindDurable, item{ID: "a"})

	s.Close()

	select {
	case <-s.Ready():
		t.Fatal("Ready fired after Close, but the queue is empty")
	default:
	}
}

func TestPublish_AssignsIncreasingSeq_UnderConcurrency(t *testing.T) {
	const n = 100
	b := New()
	s := b.Subscribe("x", Filter{To: agent("x")})

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		seqs []uint64
	)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			seq, err := Publish(b, kindDurable, orchestrator, agent("x"), OriginSystem, item{N: i})
			if err != nil {
				t.Errorf("Publish: %v", err)
				return
			}
			mu.Lock()
			seqs = append(seqs, seq)
			mu.Unlock()
		}()
	}
	wg.Wait()

	slices.Sort(seqs)
	for i, seq := range seqs {
		if seq != uint64(i+1) {
			t.Fatalf("Seq values are not unique and gapless: index %d has %d", i, seq)
		}
	}

	// The subscriber sees the same order as Seq, which proves that Seq and
	// the queue append happen under one lock.
	msgs := s.Drain()
	if len(msgs) != n {
		t.Fatalf("Drain returned %d messages, want %d", len(msgs), n)
	}
	for i, m := range msgs {
		if m.Seq != uint64(i+1) {
			t.Fatalf("message %d has Seq %d, want %d", i, m.Seq, i+1)
		}
	}
}

func TestDurable_NeverDropped(t *testing.T) {
	const n = 5000
	b := New()
	s := b.Subscribe("x", Filter{To: agent("x")})
	for i := range n {
		mustPublish(t, b, kindDurable, item{N: i})
	}

	got := decodeItems(t, s.Drain())
	if len(got) != n {
		t.Fatalf("Drain returned %d messages, want %d", len(got), n)
	}
	for i, v := range got {
		if v.N != i {
			t.Fatalf("message %d has N=%d, want %d", i, v.N, i)
		}
	}
}

func TestLatest_KeepsNewestPerKey(t *testing.T) {
	b := New()
	s := b.Subscribe("x", Filter{To: agent("x")})
	mustPublish(t, b, kindLatest, item{ID: "a", N: 1})
	mustPublish(t, b, kindLatest, item{ID: "b", N: 1})
	mustPublish(t, b, kindLatest, item{ID: "a", N: 2})
	mustPublish(t, b, kindLatest, item{ID: "a", N: 3})

	// First-seen order of the keys: a, then b.
	got := decodeItems(t, s.Drain())
	want := []item{{ID: "a", N: 3}, {ID: "b", N: 1}}
	if !slices.Equal(got, want) {
		t.Fatalf("Drain = %+v, want %+v", got, want)
	}
}

func TestLatest_KeyIsNamespacedByKind(t *testing.T) {
	b := New()
	s := b.Subscribe("x", Filter{To: agent("x")})
	mustPublish(t, b, kindLatest, item{ID: "a", N: 1})
	mustPublish(t, b, kindLatestOther, item{ID: "a", N: 2})

	if got := s.Drain(); len(got) != 2 {
		t.Fatalf("Drain returned %d messages, want 2: two kinds with one key must not replace each other", len(got))
	}
}

func TestDrain_DurableBeforeLatest(t *testing.T) {
	b := New()
	s := b.Subscribe("x", Filter{To: agent("x")})
	mustPublish(t, b, kindLatest, item{ID: "a", N: 1})
	mustPublish(t, b, kindDurable, item{ID: "d", N: 2})

	got := s.Drain()
	if len(got) != 2 {
		t.Fatalf("Drain returned %d messages, want 2", len(got))
	}
	if got[0].Kind != kindDurable || got[1].Kind != kindLatest {
		t.Fatalf("Drain kinds = %v %v, want durable then latest", got[0].Kind, got[1].Kind)
	}
}

func TestReady_SignalCoalesces(t *testing.T) {
	b := New()
	s := b.Subscribe("x", Filter{To: agent("x")})
	for i := range 1000 {
		mustPublish(t, b, kindDurable, item{N: i})
	}

	select {
	case <-s.Ready():
	default:
		t.Fatal("no Ready signal after publish")
	}
	select {
	case <-s.Ready():
		t.Fatal("second Ready signal for one batch of messages")
	default:
	}

	if got := s.Drain(); len(got) != 1000 {
		t.Fatalf("Drain returned %d messages, want 1000", len(got))
	}
	select {
	case <-s.Ready():
		t.Fatal("Ready fired after Drain took every message")
	default:
	}

	mustPublish(t, b, kindDurable, item{N: 1})
	select {
	case <-s.Ready():
	default:
		t.Fatal("no Ready signal for a message after Drain")
	}
}

func TestPublish_NeverBlocks_SlowSubscriber(t *testing.T) {
	b := New()
	b.Subscribe("slow", Filter{To: agent("x")}) // never drained

	start := time.Now()
	for i := range 10000 {
		mustPublish(t, b, kindDurable, item{N: i})
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("10000 publishes took %v, want under 1s", elapsed)
	}
}

func TestSubscribe_KindsFilter(t *testing.T) {
	b := New()
	s := b.Subscribe("x", Filter{To: agent("x"), Kinds: []Kind{kindLatest}})
	mustPublish(t, b, kindDurable, item{ID: "d"})
	mustPublish(t, b, kindLatest, item{ID: "l"})

	got := s.Drain()
	if len(got) != 1 || got[0].Kind != kindLatest {
		t.Fatalf("Drain = %+v, want only the latest-kind message", got)
	}
}

func TestPublish_DurableKindIgnoresKeyFunction(t *testing.T) {
	const kind Kind = "test.durable-keyed"
	Register[item](kind, Durable, func(item) string {
		panic("key function called for a Durable kind")
	})

	b := New()
	s := b.Subscribe("x", Filter{To: agent("x")})
	if _, err := Publish(b, kind, orchestrator, agent("x"), OriginSystem, item{ID: "a"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if got := s.Drain(); len(got) != 1 {
		t.Fatalf("Drain returned %d messages, want 1", len(got))
	}
}

func TestSub_Name(t *testing.T) {
	s := New().Subscribe("tui", Filter{To: Addr{Type: AddrTUI}})
	if s.Name() != "tui" {
		t.Fatalf("Name() = %q, want tui", s.Name())
	}
}

func TestRegister_SameClassTwice_DoesNothing(t *testing.T) {
	Register[item](kindDurable, Durable, nil)
}

func TestRegister_DifferentClass_Panics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Register with another class did not panic")
		}
	}()
	Register[item](kindDurable, Latest, itemKey)
}

func TestJSONRoundTrip(t *testing.T) {
	b := New()
	s := b.Subscribe("x", Filter{To: agent("x")})
	seq, err := Publish(b, kindLatest, agent("a"), agent("x"), OriginAgent, item{ID: "k", N: 7})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	msgs := s.Drain()
	if len(msgs) != 1 || msgs[0].Seq != seq {
		t.Fatalf("Drain = %+v, want one message with Seq %d", msgs, seq)
	}
	orig := msgs[0]
	orig.ReplyTo = 41 // a fixed non-zero value, so omitempty cannot hide a lost field

	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got Message
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if !got.At.Equal(orig.At) {
		t.Fatalf("At = %v, want %v", got.At, orig.At)
	}
	got.At = orig.At // the monotonic clock reading does not survive JSON
	if !reflect.DeepEqual(got.From, orig.From) || !reflect.DeepEqual(got.To, orig.To) ||
		got.Seq != orig.Seq || got.Kind != orig.Kind || got.ReplyTo != orig.ReplyTo ||
		got.Origin != orig.Origin || string(got.Payload) != string(orig.Payload) {
		t.Fatalf("round trip changed the message:\n got %+v\nwant %+v", got, orig)
	}

	decoded, err := Decode[item](got)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if decoded != (item{ID: "k", N: 7}) {
		t.Fatalf("Decode = %+v, want {k 7}", decoded)
	}
}

func TestSubDone_ClosedBySubClose(t *testing.T) {
	b := New()
	s := b.Subscribe("s", Filter{To: agent("a")})
	s.Close()
	select {
	case <-s.Done():
	case <-time.After(time.Second):
		t.Fatal("Done is not closed after Sub.Close")
	}
	s.Close() // a second Close must not panic
}

func TestSubDone_ClosedByBusClose_WakesWaiter(t *testing.T) {
	b := New()
	s := b.Subscribe("s", Filter{To: agent("a")})
	woke := make(chan struct{})
	go func() {
		select {
		case <-s.Ready():
		case <-s.Done():
		}
		close(woke)
	}()

	b.Close()
	b.Close() // Close is idempotent
	select {
	case <-woke:
	case <-time.After(time.Second):
		t.Fatal("waiter did not return after Bus.Close")
	}
}

func TestSubDone_OnClosedBusIsClosed(t *testing.T) {
	b := New()
	b.Close()
	s := b.Subscribe("late", Filter{To: agent("a")})
	select {
	case <-s.Done():
	default:
		t.Fatal("Done of a subscription made on a closed bus is open")
	}
}

func TestSubCloseAndDrain_ReturnsQueuedMessages(t *testing.T) {
	b := New()
	s := b.Subscribe("s", Filter{To: agent("a")})
	mustPublishTo(t, b, agent("a"), item{ID: "1"})
	mustPublishTo(t, b, agent("a"), item{ID: "2"})

	got := decodeItems(t, s.CloseAndDrain())
	if want := []item{{ID: "1"}, {ID: "2"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("CloseAndDrain = %v, want %v", got, want)
	}
	mustPublishTo(t, b, agent("a"), item{ID: "3"})
	if msgs := s.Drain(); msgs != nil {
		t.Fatalf("closed sub got %d messages after CloseAndDrain, want none", len(msgs))
	}
	select {
	case <-s.Done():
	default:
		t.Fatal("Done is open after CloseAndDrain")
	}
}

// mustPublishTo publishes item p to agent a from the orchestrator.
func mustPublishTo(t *testing.T, b *Bus, to Addr, p item) {
	t.Helper()
	if _, err := Publish(b, kindDurable, orchestrator, to, OriginSystem, p); err != nil {
		t.Fatalf("Publish: %v", err)
	}
}

func TestSubAccepted_CountsStoredMessages(t *testing.T) {
	b := New()
	s := b.Subscribe("s", Filter{To: agent("a")})
	mustPublishTo(t, b, agent("a"), item{ID: "1"})
	mustPublishTo(t, b, agent("a"), item{ID: "2"})
	s.Drain()
	if got := s.Accepted(); got != 2 {
		t.Fatalf("Accepted = %d, want 2 after Drain", got)
	}
}

func TestForward_ToOrchestratorKeepsOrigTo(t *testing.T) {
	b := New()
	orch := b.Subscribe("orch", Filter{To: orchestrator})
	inbox := b.Subscribe("inbox", Filter{To: agent("a")})
	mustPublishTo(t, b, agent("a"), item{ID: "left"})
	msgs := inbox.CloseAndDrain()
	if len(msgs) != 1 {
		t.Fatalf("inbox held %d messages, want 1", len(msgs))
	}
	if _, err := b.Forward(msgs[0], orchestrator); err != nil {
		t.Fatalf("Forward: %v", err)
	}
	got := orch.Drain()
	if len(got) != 1 {
		t.Fatalf("orchestrator got %d messages, want 1", len(got))
	}
	if got[0].OrigTo == nil || *got[0].OrigTo != agent("a") || got[0].Origin != OriginSystem {
		t.Fatalf("forwarded message = OrigTo %+v Origin %q, want agent a and system", got[0].OrigTo, got[0].Origin)
	}
	if v, err := Decode[item](got[0]); err != nil || v.ID != "left" {
		t.Fatalf("payload = %+v, %v; want ID left", v, err)
	}
}

func TestForward_NonDurableRejected(t *testing.T) {
	b := New()
	_, err := Publish(b, kindLatest, orchestrator, agent("a"), OriginSystem, item{ID: "k"})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	m := Message{Kind: kindLatest, To: agent("a")}
	if _, err := b.Forward(m, orchestrator); err == nil {
		t.Fatal("Forward of a Latest kind succeeded, want error")
	}
}
