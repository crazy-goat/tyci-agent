package pricing

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const nexosBody = `{"data":[
 {"id":"DeepSeek V4.1 Flash","context_length":128000,"max_tokens":8000,
  "pricing":{"input_cost_per_token":"0.0000003","output_cost_per_token":"0.0000012","cache_read_cost_per_token":"0.00000003"}},
 {"id":"Claude Sonnet 5","context_length":150000,
  "pricing":{"input_cost_per_token":"0.0000033"}}]}`

func withNexos(t *testing.T, catalogBody string) *int32 {
	t.Helper()
	withCatalog(t, catalogBody)
	t.Setenv("NEXOS_API_KEY", "secret-key")
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.Header.Get("Authorization") != "Bearer secret-key" || r.URL.Path != "/models" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(nexosBody))
	}))
	t.Cleanup(srv.Close)
	old := nexosAPIURL
	nexosAPIURL = func() string { return srv.URL }
	t.Cleanup(func() { nexosAPIURL = old })
	return &hits
}

func TestLookup_NexosAPIWinsAndFillsUnlisted(t *testing.T) {
	hits := withNexos(t, `{"nexos":{"id":"nexos","models":{"Claude Sonnet 5":{"id":"Claude Sonnet 5",
	  "cost":{"input":1,"output":9,"cache_read":0.5},"limit":{"context":200000,"output":64000}}}}}`)
	Lookup("nexos", "DeepSeek V4.1 Flash")
	nexosWG.Wait()
	r, l := Lookup("nexos", "DeepSeek V4.1 Flash")
	if r.Input != 0.3 || r.Output != 1.2 || r.CacheRead != 0.03 || l.Context != 128000 {
		t.Fatalf("unlisted model: %+v %+v", r, l)
	}
	r, l = Lookup("nexos", "claude sonnet 5")
	if r.Input != 3.3 {
		t.Fatalf("API must win, got input %v", r.Input)
	}
	if r.Output != 9 || r.CacheRead != 0.5 || l.Output != 64000 || l.Context != 150000 {
		t.Fatalf("catalog must fill gaps: %+v %+v", r, l)
	}
	if *hits != 1 {
		t.Fatalf("API hits = %d, want 1", *hits)
	}
}

func TestLookup_NexosUsesDiskCache(t *testing.T) {
	hits := withNexos(t, "")
	Lookup("nexos", "DeepSeek V4.1 Flash")
	nexosWG.Wait()
	Reset()
	r, _ := Lookup("nexos", "DeepSeek V4.1 Flash")
	if !r.Known() || *hits != 1 {
		t.Fatalf("rates %+v, hits %d", r, *hits)
	}
}

func TestLookup_NexosFailureKeepsCostUnknownAndHidesKey(t *testing.T) {
	withNexos(t, "")
	t.Setenv("NEXOS_API_KEY", "wrong")
	Lookup("nexos", "DeepSeek V4.1 Flash")
	nexosWG.Wait()
	r, _ := Lookup("nexos", "DeepSeek V4.1 Flash")
	if r.Known() {
		t.Fatal("failed API must leave cost unknown")
	}
	if _, err := fetchNexos(nexosAPIURL(), "wrong"); err == nil || strings.Contains(err.Error(), "wrong") {
		t.Fatalf("error must not leak the key: %v", err)
	}
}

func TestLookup_NexosNoKeyNoRequest(t *testing.T) {
	hits := withNexos(t, "")
	t.Setenv("NEXOS_API_KEY", "")
	if r, _ := Lookup("nexos", "DeepSeek V4.1 Flash"); r.Known() || *hits != 0 {
		t.Fatalf("rates %+v hits %d", r, *hits)
	}
	if _, err := os.Stat(nexosCachePath()); err == nil {
		t.Fatal("no cache expected")
	}
}

func TestLookup_NexosSlowAPIDoesNotBlock(t *testing.T) {
	withCatalog(t, "")
	t.Setenv("NEXOS_API_KEY", "secret-key")
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release); nexosWG.Wait() })
	old := nexosAPIURL
	nexosAPIURL = func() string { return srv.URL }
	t.Cleanup(func() { nexosAPIURL = old })
	start := time.Now()
	r, _ := Lookup("nexos", "DeepSeek V4.1 Flash")
	if time.Since(start) > time.Second || r.Known() {
		t.Fatalf("Lookup blocked or invented a price: %v %+v", time.Since(start), r)
	}
}
