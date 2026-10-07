package fake_test

import (
	"context"
	"errors"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/forge"
	"github.com/crazy-goat/tyci-agent/internal/forge/fake"
	"github.com/crazy-goat/tyci-agent/internal/forge/forgetest"
)

func TestFake_Contract(t *testing.T) {
	forgetest.RunContract(t, func(_ *testing.T, fx forgetest.Fixture) forge.Forge {
		return &fake.Fake{Ms: fx.Milestones, Is: fx.Issues, Writers: fx.Writers}
	})
}

func TestFake_ErrPropagates(t *testing.T) {
	boom := errors.New("boom")
	f := &fake.Fake{Err: boom}
	ctx := context.Background()
	if _, err := f.Milestones(ctx); !errors.Is(err, boom) {
		t.Error(err)
	}
	if _, err := f.Issues(ctx, ""); !errors.Is(err, boom) {
		t.Error(err)
	}
	if _, err := f.CanWrite(ctx, "a"); !errors.Is(err, boom) {
		t.Error(err)
	}
}
