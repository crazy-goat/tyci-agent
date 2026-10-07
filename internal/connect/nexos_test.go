package connect

import "testing"

func TestParseNexosModels(t *testing.T) {
	body := []byte(`{"data":[
	 {"id":"GPT 6 Luna","name":"GPT 6 Luna","context_length":922000,"max_tokens":128000,
	  "pricing":{"input_cost_per_token":"0.00000012","output_cost_per_token":"0.0000006",
	  "cache_read_cost_per_token":"0.000000012","cache_write_cost_per_token":"0.00000015"}},
	 {"id":"embed","pricing":{"input_cost_per_token":"0.00000013"}}]}`)
	p, err := ParseNexosModels(body)
	if err != nil {
		t.Fatal(err)
	}
	m := p.Models["GPT 6 Luna"]
	if m.Limit.Context != 922000 || m.Limit.Output != 128000 {
		t.Fatalf("limit = %+v", m.Limit)
	}
	if !near(m.Cost.Input, 0.12) || !near(m.Cost.Output, 0.6) || !near(m.Cost.CacheRead, 0.012) || !near(m.Cost.CacheWrite, 0.15) {
		t.Fatalf("cost = %+v", m.Cost)
	}
	if len(p.Models) != 2 {
		t.Fatalf("models = %d", len(p.Models))
	}
}

func near(a, b float64) bool { d := a - b; return d < 1e-9 && d > -1e-9 }
