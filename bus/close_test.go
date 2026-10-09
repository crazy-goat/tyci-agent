package bus

import (
	"bytes"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
)

// TestClose_RacesPublish_EveryAcceptedJournalLineIsWritten closes the bus
// while publishers are still running. Each Publish that returns no error must
// have its journal line on disk, and each one after Close must return ErrClosed.
func TestClose_RacesPublish_EveryAcceptedJournalLineIsWritten(t *testing.T) {
	path := journalPath(t)
	b := New(WithJournal(path))
	var accepted atomic.Int64
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 200 {
				_, err := Publish(b, kindDurable, orchestrator, agent("x"), OriginSystem, item{ID: "a"})
				switch {
				case err == nil:
					accepted.Add(1)
				case !errors.Is(err, ErrClosed):
					t.Errorf("Publish: %v, want nil or ErrClosed", err)
				}
			}
		})
	}
	b.Close()
	wg.Wait()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if lines := int64(bytes.Count(data, []byte("\n"))); lines != accepted.Load() {
		t.Fatalf("journal lines = %d, accepted publishes = %d; want equal", lines, accepted.Load())
	}
}
