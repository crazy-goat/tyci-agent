package openai

import (
	"context"
	"fmt"
	"os"

	"github.com/decodo/tyci-agent/providers"
)

type provider struct{}

func init() {
	providers.Register(&provider{})
}

func (p *provider) Name() string {
	return "openai"
}

func (p *provider) IsConfigured() bool {
	key := os.Getenv("OPENAI_API_KEY")
	return key != ""
}

func (p *provider) Models() []string {
	return []string{}
}

func (p *provider) Send(ctx context.Context, model, prompt, system string, handler providers.StreamHandler) error {
	handler.Error(fmt.Errorf("openai provider not implemented"))
	handler.End()
	return nil
}
