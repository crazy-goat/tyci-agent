package main

import (
	"context"
	"os"
	"os/signal"
	"sync/atomic"
	"testing"
	"time"
)

func TestWatchInterrupt_ConsumesRepeatedSIGINT(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	var canceled atomic.Int32
	sigCh, sigDone := watchInterrupt(ctx, func() {
		canceled.Add(1)
	})
	t.Cleanup(func() {
		signalStopAndWait(sigCh, sigDone, stop)
	})

	sigCh <- os.Interrupt
	waitForInterruptCount(t, &canceled, 1)

	sigCh <- os.Interrupt
	waitForInterruptCount(t, &canceled, 2)

	select {
	case <-sigDone:
		t.Fatal("interrupt watcher stopped after the first SIGINT")
	default:
	}
}

func signalStopAndWait(sigCh chan os.Signal, sigDone chan struct{}, stop context.CancelFunc) {
	signal.Stop(sigCh)
	stop()
	<-sigDone
}

func waitForInterruptCount(t *testing.T, got *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for got.Load() != want {
		select {
		case <-deadline.C:
			t.Fatalf("watcher handled %d SIGINTs, want %d", got.Load(), want)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}
