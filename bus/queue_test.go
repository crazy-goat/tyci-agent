package bus

import "testing"

func TestLatest_OlderSeqDoesNotReplaceNewer(t *testing.T) {
	q := newQueue()
	q.put(Message{Seq: 5, Kind: kindLatest, Payload: []byte(`{"id":"a","n":5}`)}, Latest, "a")
	q.put(Message{Seq: 3, Kind: kindLatest, Payload: []byte(`{"id":"a","n":3}`)}, Latest, "a")

	got := q.drain()
	if len(got) != 1 || got[0].Seq != 5 {
		t.Fatalf("drain = %+v, want the message with Seq 5", got)
	}
}

func TestQueue_ClosedQueueIgnoresPut(t *testing.T) {
	q := newQueue()
	q.close()
	q.put(Message{Seq: 1, Kind: kindDurable}, Durable, "")

	if got := q.drain(); got != nil {
		t.Fatalf("drain of a closed queue = %+v, want nothing", got)
	}
}
