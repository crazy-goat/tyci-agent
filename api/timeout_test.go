package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newReq(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), "GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestDoWithTimeouts_FirstByte(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	SetTimeouts(50*time.Millisecond, time.Second)
	defer SetTimeouts(0, 0)
	_, err := doWithTimeouts(nil, newReq(t, srv.URL))
	var re *RetryableError
	if !errors.As(err, &re) {
		t.Fatalf("want RetryableError, got %v", err)
	}
}

func TestDoWithTimeouts_StreamIdle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("data: x\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	SetTimeouts(time.Second, 50*time.Millisecond)
	defer SetTimeouts(0, 0)
	resp, err := doWithTimeouts(nil, newReq(t, srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, err = io.ReadAll(resp.Body)
	var re *RetryableError
	if !errors.As(err, &re) {
		t.Fatalf("want RetryableError, got %v", err)
	}
}

func TestDoWithTimeouts_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello"))
	}))
	defer srv.Close()
	resp, err := doWithTimeouts(nil, newReq(t, srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil || string(b) != "hello" {
		t.Fatalf("got %q, %v", b, err)
	}
}
