package gitlab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/redact"
)

const (
	timeout = 30 * time.Second
	// maxPages stops a server that always sends X-Next-Page.
	maxPages = 100
	// maxBody is the largest response body the client reads.
	maxBody = 16 << 20
)

// client is a small GitLab REST v4 client. It sends the token in the
// PRIVATE-TOKEN header, follows no redirects and does not retry.
type client struct {
	base  string // for example "https://gitlab.example.com"
	token string
	http  *http.Client
}

func newClient(base, token string) *client {
	return &client{
		base:  strings.TrimRight(base, "/"),
		token: token,
		http: &http.Client{
			Timeout: timeout,
			// A redirect would send PRIVATE-TOKEN to the new host.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// apiError is a response with a status other than 2xx.
type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func isStatus(err error, status int) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.status == status
}

// do sends one request to /api/v4/<path>. path must already be escaped.
// Every error text goes through redact.Redact.
func (c *client) do(ctx context.Context, method, path string, q url.Values) ([]byte, http.Header, error) {
	u := c.base + "/api/v4/" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("gitlab: %s", redact.Redact(err.Error()))
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("gitlab: %s", redact.Redact(err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, nil, fmt.Errorf("gitlab: %s %s: %s", method, path, redact.Redact(err.Error()))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Redact the whole body before the cut, so no part of a secret stays.
		text := []rune(strings.TrimSpace(redact.Redact(string(body))))
		if len(text) > 300 {
			text = text[:300]
		}
		msg := fmt.Sprintf("gitlab: %s %s: %s: %s", method, path, resp.Status, string(text))
		return nil, nil, &apiError{status: resp.StatusCode, msg: redact.Redact(msg)}
	}
	return body, resp.Header, nil
}

// getAll sends GET for every page and returns the page bodies. It follows
// X-Next-Page until it is empty. It changes q.
func (c *client) getAll(ctx context.Context, path string, q url.Values) ([][]byte, error) {
	if q == nil {
		q = url.Values{}
	}
	q.Set("per_page", "100")
	var pages [][]byte
	for page := "1"; page != ""; {
		if len(pages) == maxPages {
			return nil, fmt.Errorf("gitlab: GET %s: more than %d pages", path, maxPages)
		}
		q.Set("page", page)
		body, h, err := c.do(ctx, http.MethodGet, path, q)
		if err != nil {
			return nil, err
		}
		pages = append(pages, body)
		next := strings.TrimSpace(h.Get("X-Next-Page"))
		if next != "" {
			if _, err := strconv.Atoi(next); err != nil {
				return nil, fmt.Errorf("gitlab: GET %s: bad X-Next-Page %q", path, next)
			}
		}
		page = next
	}
	return pages, nil
}
