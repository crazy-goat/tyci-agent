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

// doer picks the client for one request: an explicitly injected HTTPDoer wins,
// otherwise the shared default client is used.
//
// Injection is the only override left. It arrives as a field on the streamer,
// filled from connector.Endpoint.HTTP, which providers.NewProvider populates
// from its Deps.HTTP — so a caller that needs its own transport or its own
// connection pool says so when it builds the provider. There is deliberately
// no context read here any more: a request's transport is a construction-time
// decision, not something an arbitrary ancestor can swap out invisibly.
//
// A typed-nil *http.Client is treated as "nothing injected" rather than being
// used and panicking inside net/http; the removed context-key lookup guarded
// the same case.
func doer(h HTTPDoer) HTTPDoer {
	if cl, ok := h.(*http.Client); ok && cl == nil {
		return defaultClient
	}
	if h != nil {
		return h
	}
	return defaultClient
}

// applyExtraHeaders sets caller-supplied headers on req. It runs AFTER the
// protocol defaults, so an Endpoint can override e.g. Authorization; with an
// empty map (today's only case) it is a no-op and the wire bytes are unchanged.
func applyExtraHeaders(req *http.Request, extra map[string]string) {
	for k, v := range extra {
		req.Header.Set(k, v)
	}
}

// defaultClient is the shared HTTP client used by every streamer that was not
// given one of its own. It reuses connections and avoids allocating a new
// Transport per request.
//
// It is read directly by doer, with no indirection in between. There used to
// be a defaultClientProvider function variable here whose only reason to exist
// was letting one test swap the client out; a mutable package-level seam is
// safe only as long as nobody in this package calls t.Parallel(), which is a
// guarantee nothing enforces. The test in question reaches a httptest server
// over plain HTTP on 127.0.0.1, which this client can do unaided.
var defaultClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConns:        4,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     90 * time.Second,
	},
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
