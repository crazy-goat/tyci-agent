package tools

import (
	"context"
	"testing"
	"time"
)

// TestTicketReleasedOnEveryPath checks that a ticket that never reaches
// AcquireMutation does not block the FIFO, and that a cancelled waiter leaves it.
func TestTicketReleasedOnEveryPath(t *testing.T) {
	ctx := WithWorkdir(context.Background(), t.TempDir())

	never := ReserveMutation(ctx, "write", nil) // granted; the call never starts
	cancelled := ReserveMutation(ctx, "write", nil)
	if cancelled == nil || never == nil {
		t.Fatal("write must take a ticket")
	}
	never.Release()     // hands the turn to cancelled
	cancelled.Release() // it was granted, so this frees the FIFO
	cancelled.Release() // a second release does nothing

	next := ReserveMutation(ctx, "write", nil)
	done := make(chan func(), 1)
	go func() { done <- AcquireMutation(WithMutationTicket(ctx, next)) }()
	select {
	case rel := <-done:
		if rel == nil {
			t.Fatal("next ticket was not granted")
		}
		rel()
	case <-time.After(2 * time.Second):
		t.Fatal("FIFO stayed blocked after every ticket was released")
	}
}

// TestReserveSkipsNonMutations checks that reads and bash with parallel=true
// take no ticket.
func TestReserveSkipsNonMutations(t *testing.T) {
	ctx := WithWorkdir(context.Background(), t.TempDir())
	if ReserveMutation(ctx, "read", nil) != nil {
		t.Error("read took a ticket")
	}
	if ReserveMutation(ctx, "bash", map[string]any{"parallel": true}) != nil {
		t.Error("bash parallel=true took a ticket")
	}
	if ReserveMutation(ctx, "bash", map[string]any{}) == nil {
		t.Error("bash without parallel took no ticket")
	}
}
