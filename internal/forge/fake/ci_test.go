package fake

import (
	"context"
	"errors"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/forge"
)

func TestCI(t *testing.T) {
	var c CI
	ctx := context.Background()
	if _, ok, err := c.LatestCI(ctx, "w", "main"); ok || err != nil {
		t.Fatalf("empty: ok=%v err=%v", ok, err)
	}
	c.SetRun("w", "main", forge.CIRun{ID: 1, SHA: "a", Failed: true})
	if r, ok, err := c.LatestCI(ctx, "w", "main"); !ok || err != nil || r.SHA != "a" {
		t.Fatalf("run %+v ok=%v err=%v", r, ok, err)
	}
	boom := errors.New("boom")
	c.SetErr(boom)
	if _, _, err := c.LatestCI(ctx, "w", "main"); !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
	if err := c.Comment(ctx, 3, "hi"); err != nil {
		t.Fatal(err)
	}
	if got := c.Comments(); len(got) != 1 || got[0] != (Comment{3, "hi"}) {
		t.Fatalf("comments %+v", got)
	}
}
