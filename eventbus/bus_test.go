package eventbus

import (
	"testing"
	"time"
)

func TestSubscribePublishReceive(t *testing.T) {
	b := New(4)
	defer b.Close()

	ch, unsub := b.Subscribe("topic")
	defer unsub()

	b.Publish("topic", "hello")

	select {
	case evt := <-ch:
		if evt.Topic != "topic" || evt.Payload != "hello" {
			t.Fatalf("unexpected event: %+v", evt)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestFanOutMultipleSubscribers(t *testing.T) {
	b := New(4)
	defer b.Close()

	ch1, unsub1 := b.Subscribe("topic")
	defer unsub1()
	ch2, unsub2 := b.Subscribe("topic")
	defer unsub2()

	b.Publish("topic", 42)

	for _, ch := range []<-chan Event{ch1, ch2} {
		select {
		case evt := <-ch:
			if evt.Payload != 42 {
				t.Fatalf("unexpected payload: %v", evt.Payload)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for event")
		}
	}
}

func TestPublishDoesNotBlockWhenBufferFull(t *testing.T) {
	b := New(1)
	defer b.Close()

	ch, unsub := b.Subscribe("topic")
	defer unsub()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Fill the buffer, then publish more without anyone reading.
		b.Publish("topic", 1)
		b.Publish("topic", 2)
		b.Publish("topic", 3)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish blocked on a full subscriber channel")
	}

	// Only the first event should have made it into the buffer.
	select {
	case evt := <-ch:
		if evt.Payload != 1 {
			t.Fatalf("unexpected payload: %v", evt.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for buffered event")
	}

	select {
	case evt, ok := <-ch:
		if ok {
			t.Fatalf("expected no further buffered events, got: %+v", evt)
		}
	default:
	}
}

func TestUnsubscribeClosesChannelAndIsIdempotent(t *testing.T) {
	b := New(4)
	defer b.Close()

	ch, unsub := b.Subscribe("topic")

	unsub()

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected channel to be closed after unsubscribe")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for closed channel")
	}

	// Should not panic.
	unsub()

	// Publishing after unsubscribe should not reach the (now unknown) channel.
	b.Publish("topic", "ignored")
}

func TestPublishUnknownTopicDoesNotPanic(t *testing.T) {
	b := New(4)
	defer b.Close()

	b.Publish("does-not-exist", "noop")
}

func TestCloseClosesAllSubscribersAndIsSafeAfter(t *testing.T) {
	b := New(4)

	ch1, _ := b.Subscribe("a")
	ch2, _ := b.Subscribe("b")

	b.Close()

	for _, ch := range []<-chan Event{ch1, ch2} {
		select {
		case v, ok := <-ch:
			if ok {
				t.Fatalf("expected closed channel, got value: %+v", v)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for closed channel")
		}
	}

	// Should not panic.
	b.Close()
	b.Publish("a", "noop")

	ch3, unsub3 := b.Subscribe("c")
	defer unsub3()
	select {
	case _, ok := <-ch3:
		if ok {
			t.Fatal("expected channel from post-close Subscribe to be closed")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for closed channel from post-close Subscribe")
	}
}
