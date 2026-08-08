package api

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// HTTPDoer is the minimal HTTP surface the streamers need. Taking an
// interface (instead of *http.Client) is what makes the transport injectable
// per streamer, and what lets tests hand in a recording double.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// doer picks the client for one request: an explicitly injected HTTPDoer
// always wins, otherwise the context decides (and falls back to the shared
// default client).
//
// The ClientFromContext fallback is scheduled for removal in Etap 4 of
// docs/architecture-refactor.md, once providers.Provider becomes a struct
// with its own HTTP field and can inject a doer when it builds the connector.
// Until then the context is the ONLY injection path in the running program
// (tools/subagent.go builds an isolated connection pool that way), so
// dropping the fallback here would silently break it.
func doer(ctx context.Context, h HTTPDoer) HTTPDoer {
	if h != nil {
		return h
	}
	return ClientFromContext(ctx)
}

// applyExtraHeaders sets caller-supplied headers on req. It runs AFTER the
// protocol defaults, so an Endpoint can override e.g. Authorization; with an
// empty map (today's only case) it is a no-op and the wire bytes are unchanged.
func applyExtraHeaders(req *http.Request, extra map[string]string) {
	for k, v := range extra {
		req.Header.Set(k, v)
	}
}

// defaultClient is the shared HTTP client used by all API streaming functions.
// It reuses connections and avoids allocating a new Transport per request.
var defaultClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConns:        4,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     90 * time.Second,
	},
}

// defaultClientProvider returns the shared HTTP client.
// Extracted as a variable so tests can override it.
var defaultClientProvider = func() *http.Client { return defaultClient }

// HTTPClientKey is the context key for overriding the HTTP client per-request.
// Used by subagent to create its own isolated HTTP connection pool.
type HTTPClientKey struct{}

// ClientFromContext returns an HTTP client from context if set, otherwise
// falls back to the global default client provider.
func ClientFromContext(ctx context.Context) *http.Client {
	if cl, ok := ctx.Value(HTTPClientKey{}).(*http.Client); ok && cl != nil {
		return cl
	}
	return defaultClientProvider()
}

type RetryableError struct {
	Code       int
	RetryAfter string
	Message    string
}

func (e *RetryableError) Error() string { return e.Message }

func IsRetryable(err error) bool {
	var re *RetryableError
	if errors.As(err, &re) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	return false
}

type RetryConfig struct {
	MaxRetries  int
	BaseBackoff int
	MaxBackoff  int
}

func (c RetryConfig) WithDefaults() RetryConfig {
	if c.MaxRetries == 0 {
		c.MaxRetries = 5
	}
	if c.BaseBackoff == 0 {
		c.BaseBackoff = 4
	}
	if c.MaxBackoff == 0 {
		c.MaxBackoff = 128
	}
	return c
}

func CalcBackoff(attempt int, err error, config RetryConfig) time.Duration {
	config = config.WithDefaults()
	var re *RetryableError
	if errors.As(err, &re) && re.RetryAfter != "" {
		if d, parseErr := strconv.Atoi(strings.TrimSpace(re.RetryAfter)); parseErr == nil {
			dur := time.Duration(d) * time.Second
			maxDur := time.Duration(config.MaxBackoff) * time.Second
			if dur > maxDur {
				dur = maxDur
			}
			return dur
		}
	}
	// Cap shift to prevent integer overflow on 64-bit systems.
	// Max safe shift: 62 would overflow when multiplied by BaseBackoff=4,
	// so we cap at 30 which gives 2^30 * max BaseBackoff < 2^63.
	shift := attempt
	if shift > 30 {
		shift = 30
	}
	backoff := config.BaseBackoff * (1 << shift)
	maxDur := time.Duration(config.MaxBackoff) * time.Second
	dur := time.Duration(backoff) * time.Second
	if dur > maxDur {
		dur = maxDur
	}
	return dur
}
