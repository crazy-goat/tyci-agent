package pricing

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/connect"
)

// nexosProvider is the provider name whose prices and limits come from the
// nexos API instead of models.dev, which does not list most nexos models.
const nexosProvider = "nexos"

const (
	nexosDefaultAPI = "https://api.nexos.ai/v1"
	nexosTTL        = 6 * time.Hour
	nexosTimeout    = 3 * time.Second
)

// nexosEntry is one model as the cache stores it: USD per million tokens and
// token counts. Zero means the API gave no value.
type nexosEntry struct {
	Rates  Rates  `json:"rates"`
	Limits Limits `json:"limits"`
}

type nexosCache struct {
	Fetched int64                 `json:"fetched"`
	Models  map[string]nexosEntry `json:"models"`
}

var (
	nexosMu     sync.Mutex
	nexosLoaded bool
	nexosModels map[string]nexosEntry
	nexosGen    int // bumped by Reset so an old refresh drops its result
	nexosWG     sync.WaitGroup
)

func nexosCachePath() string {
	return filepath.Join(filepath.Dir(connect.ProvidersJSONPath()), "nexos-models.json")
}

// nexosAPIURL is the base URL of the nexos API; tests replace it.
var nexosAPIURL = func() string {
	if p, ok := catalog()[nexosProvider]; ok && p.API != "" {
		return strings.TrimRight(p.API, "/")
	}
	return nexosDefaultAPI
}

func nexosKey() string {
	if k, ok, err := connect.GetKey(nexosProvider); err == nil && ok {
		if v := connect.ResolveToken(k); v != "" {
			return v
		}
	}
	return os.Getenv("NEXOS_API_KEY")
}

// nexosCatalog returns the nexos models from the disk cache at once, even
// when it is stale or missing. A stale or missing cache starts one background
// refresh per process, so a slow or dead API never blocks the caller. Any
// failure keeps the old value or nil: cost then stays unknown, as before. The
// key is only sent in the request header and never appears in an error.
func nexosCatalog() map[string]nexosEntry {
	nexosMu.Lock()
	defer nexosMu.Unlock()
	if nexosLoaded {
		return nexosModels
	}
	nexosLoaded = true
	var cached nexosCache
	if data, err := os.ReadFile(nexosCachePath()); err == nil {
		_ = json.Unmarshal(data, &cached)
	}
	nexosModels = cached.Models
	if cached.Models != nil && time.Since(time.Unix(cached.Fetched, 0)) < nexosTTL {
		return nexosModels
	}
	key := nexosKey()
	if key == "" {
		return nexosModels
	}
	gen, url, path := nexosGen, nexosAPIURL(), nexosCachePath()
	nexosWG.Add(1)
	go func() {
		defer nexosWG.Done()
		fresh, err := fetchNexos(url, key)
		if err != nil || len(fresh) == 0 {
			return
		}
		nexosMu.Lock()
		defer nexosMu.Unlock()
		if gen != nexosGen {
			return
		}
		nexosModels = fresh
		if data, err := json.Marshal(nexosCache{Fetched: time.Now().Unix(), Models: fresh}); err == nil {
			_ = os.WriteFile(path, data, 0o600)
		}
	}()
	return nexosModels
}

func fetchNexos(base, key string) (map[string]nexosEntry, error) {
	req, err := http.NewRequest(http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := (&http.Client{Timeout: nexosTimeout}).Do(req)
	if err != nil {
		return nil, errNexos
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, errNexos
	}
	var body struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength int    `json:"context_length"`
			MaxTokens     int    `json:"max_tokens"`
			Pricing       struct {
				Input      string `json:"input_cost_per_token"`
				Output     string `json:"output_cost_per_token"`
				CacheRead  string `json:"cache_read_cost_per_token"`
				CacheWrite string `json:"cache_write_cost_per_token"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, errNexos
	}
	out := make(map[string]nexosEntry, len(body.Data))
	for _, m := range body.Data {
		out[m.ID] = nexosEntry{
			Rates: Rates{
				Input:      perMillion(m.Pricing.Input),
				Output:     perMillion(m.Pricing.Output),
				CacheRead:  perMillion(m.Pricing.CacheRead),
				CacheWrite: perMillion(m.Pricing.CacheWrite),
			},
			Limits: Limits{Context: m.ContextLength, Output: m.MaxTokens},
		}
	}
	return out, nil
}

type nexosError string

func (e nexosError) Error() string { return string(e) }

const errNexos = nexosError("nexos models request failed")

// perMillion converts the API's USD-per-token string to USD per million.
func perMillion(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 {
		return 0
	}
	// Round away float noise such as 3.3000000000000003.
	return float64(int64(f*1e6*1e6+0.5)) / 1e6
}

func nexosFind(model string) (nexosEntry, bool) {
	c := nexosCatalog()
	if e, ok := c[model]; ok {
		return e, true
	}
	want := strings.ToLower(model)
	for id, e := range c {
		if strings.ToLower(id) == want {
			return e, true
		}
	}
	return nexosEntry{}, false
}

// overlayNexos lets the API values win over the catalog values, field by
// field; the catalog stays as a fallback where the API has no value.
func overlayNexos(model string, r Rates, l Limits) (Rates, Limits) {
	e, ok := nexosFind(model)
	if !ok {
		return r, l
	}
	pick := func(api, fallback float64) float64 {
		if api > 0 {
			return api
		}
		return fallback
	}
	r = Rates{
		Input:      pick(e.Rates.Input, r.Input),
		Output:     pick(e.Rates.Output, r.Output),
		CacheRead:  pick(e.Rates.CacheRead, r.CacheRead),
		CacheWrite: pick(e.Rates.CacheWrite, r.CacheWrite),
	}
	if e.Limits.Context > 0 {
		l.Context = e.Limits.Context
	}
	if e.Limits.Output > 0 {
		l.Output = e.Limits.Output
	}
	return r, l
}
