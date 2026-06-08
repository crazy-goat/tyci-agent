package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

func TestIsRetryable_RetryableError(t *testing.T) {
	err := &RetryableError{Code: 429, Message: "rate limited"}
	if !IsRetryable(err) {
		t.Error("expected RetryableError to be retryable")
	}
}

func TestIsRetryable_RetryableError5xx(t *testing.T) {
	err := &RetryableError{Code: 503, Message: "server error"}
	if !IsRetryable(err) {
		t.Error("expected 5xx RetryableError to be retryable")
	}
}

func TestIsRetryable_EOF(t *testing.T) {
	if !IsRetryable(io.EOF) {
		t.Error("expected io.EOF to be retryable")
	}
}

func TestIsRetryable_ErrUnexpectedEOF(t *testing.T) {
	if !IsRetryable(io.ErrUnexpectedEOF) {
		t.Error("expected io.ErrUnexpectedEOF to be retryable")
	}
}

func TestIsRetryable_DeadlineExceeded(t *testing.T) {
	if !IsRetryable(context.DeadlineExceeded) {
		t.Error("expected context.DeadlineExceeded to be retryable")
	}
}

func TestIsRetryable_NetError(t *testing.T) {
	var netErr net.Error = &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	if !IsRetryable(netErr) {
		t.Error("expected net.Error to be retryable")
	}
}

func TestIsRetryable_DNSError(t *testing.T) {
	err := &net.DNSError{Err: "no such host", Name: "example.com"}
	if !IsRetryable(err) {
		t.Error("expected net.DNSError to be retryable")
	}
}

func TestIsRetryable_NonRetryable(t *testing.T) {
	err := fmt.Errorf("bad request")
	if IsRetryable(err) {
		t.Error("expected plain error to not be retryable")
	}
}

func TestIsRetryable_Nil(t *testing.T) {
	if IsRetryable(nil) {
		t.Error("expected nil to not be retryable")
	}
}

func TestCalcBackoff_Exponential(t *testing.T) {
	config := RetryConfig{MaxRetries: 5, BaseBackoff: 4, MaxBackoff: 128}
	err := fmt.Errorf("server error")

	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{0, 4 * time.Second},
		{1, 8 * time.Second},
		{2, 16 * time.Second},
		{3, 32 * time.Second},
		{4, 64 * time.Second},
	}

	for _, tt := range tests {
		got := CalcBackoff(tt.attempt, err, config)
		if got != tt.expected {
			t.Errorf("attempt %d: got %v, want %v", tt.attempt, got, tt.expected)
		}
	}
}

func TestCalcBackoff_CapsAtMaxBackoff(t *testing.T) {
	config := RetryConfig{MaxRetries: 10, BaseBackoff: 4, MaxBackoff: 128}
	err := fmt.Errorf("server error")

	got := CalcBackoff(10, err, config)
	if got != 128*time.Second {
		t.Errorf("expected cap at 128s, got %v", got)
	}
}

func TestCalcBackoff_RespectsRetryAfter(t *testing.T) {
	config := RetryConfig{MaxRetries: 5, BaseBackoff: 4, MaxBackoff: 128}
	err := &RetryableError{Code: 429, RetryAfter: "10", Message: "rate limited"}

	got := CalcBackoff(0, err, config)
	if got != 10*time.Second {
		t.Errorf("expected 10s from Retry-After, got %v", got)
	}
}

func TestCalcBackoff_RetryAfterCapsAtMaxBackoff(t *testing.T) {
	config := RetryConfig{MaxRetries: 5, BaseBackoff: 4, MaxBackoff: 128}
	err := &RetryableError{Code: 429, RetryAfter: "200", Message: "rate limited"}

	got := CalcBackoff(0, err, config)
	if got != 128*time.Second {
		t.Errorf("expected cap at 128s, got %v", got)
	}
}

func TestCalcBackoff_RetryAfterInvalid(t *testing.T) {
	config := RetryConfig{MaxRetries: 5, BaseBackoff: 4, MaxBackoff: 128}
	err := &RetryableError{Code: 429, RetryAfter: "invalid", Message: "rate limited"}

	got := CalcBackoff(0, err, config)
	if got != 4*time.Second {
		t.Errorf("expected fallback to base backoff 4s, got %v", got)
	}
}

func TestRetryConfig_WithDefaults(t *testing.T) {
	config := RetryConfig{}.WithDefaults()
	if config.MaxRetries != 5 {
		t.Errorf("expected MaxRetries=5, got %d", config.MaxRetries)
	}
	if config.BaseBackoff != 4 {
		t.Errorf("expected BaseBackoff=4, got %d", config.BaseBackoff)
	}
	if config.MaxBackoff != 128 {
		t.Errorf("expected MaxBackoff=128, got %d", config.MaxBackoff)
	}
}

func TestRetryConfig_WithDefaults_PreservesValues(t *testing.T) {
	config := RetryConfig{MaxRetries: 3, BaseBackoff: 2, MaxBackoff: 64}.WithDefaults()
	if config.MaxRetries != 3 {
		t.Errorf("expected MaxRetries=3, got %d", config.MaxRetries)
	}
	if config.BaseBackoff != 2 {
		t.Errorf("expected BaseBackoff=2, got %d", config.BaseBackoff)
	}
	if config.MaxBackoff != 64 {
		t.Errorf("expected MaxBackoff=64, got %d", config.MaxBackoff)
	}
}

func TestCalcBackoff_HighAttemptDoesNotOverflow(t *testing.T) {
	config := RetryConfig{MaxRetries: 100, BaseBackoff: 4, MaxBackoff: 128}
	err := fmt.Errorf("server error")

	tests := []struct {
		attempt int
	}{
		{31},
		{62},
		{63},
		{100},
	}

	for _, tt := range tests {
		got := CalcBackoff(tt.attempt, err, config)
		// Must not be 0, must not be negative
		if got <= 0 {
			t.Errorf("attempt %d: got %v, want > 0", tt.attempt, got)
		}
		// Must be capped at MaxBackoff
		if got > 128*time.Second {
			t.Errorf("attempt %d: got %v, want capped at 128s", tt.attempt, got)
		}
	}
}

func TestCalcBackoff_ExactOverflowBoundaries(t *testing.T) {
	config := RetryConfig{MaxRetries: 100, BaseBackoff: 4, MaxBackoff: 999999}
	err := fmt.Errorf("server error")

	// At attempt=30, base*2^30 = 4*1073741824 = 4294967296s, which fits in int64
	got := CalcBackoff(30, err, config)
	if got <= 0 {
		t.Errorf("attempt 30: got %v, want > 0", got)
	}
}
