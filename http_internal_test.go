package jev

import (
	"net/http"
	"testing"
	"time"
)

func header(pairs ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(pairs); i += 2 {
		h.Set(pairs[i], pairs[i+1])
	}
	return h
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		h    http.Header
		want time.Duration
	}{
		"empty":       {header(), 0},
		"seconds":     {header("Retry-After", "3"), 3 * time.Second},
		"fractional":  {header("Retry-After", "1.5"), 1500 * time.Millisecond},
		"negative":    {header("Retry-After", "-1"), 0},
		"unparsable":  {header("Retry-After", "bad"), 0},
		"date":        {header("Retry-After", now.Add(10*time.Second).Format(http.TimeFormat)), 10 * time.Second},
		"past date":   {header("Retry-After", now.Add(-time.Hour).Format(http.TimeFormat)), 0},
		"ms header":   {header("Retry-After-Ms", "250"), 250 * time.Millisecond},
		"ms wins":     {header("Retry-After-Ms", "250", "Retry-After", "30"), 250 * time.Millisecond},
		"ms invalid":  {header("Retry-After-Ms", "nope", "Retry-After", "2"), 2 * time.Second},
		"ms negative": {header("Retry-After-Ms", "-5", "Retry-After", "2"), 2 * time.Second},
	}
	for name, tc := range cases {
		if got := parseRetryAfter(tc.h, now); got != tc.want {
			t.Errorf("%s: parseRetryAfter = %v, want %v", name, got, tc.want)
		}
	}
}

func TestRetryAfterIsHonouredUpToTheCap(t *testing.T) {
	p := &httpProvider{retry: Retry{
		MaxRetries: 3, BackoffInitial: time.Second, BackoffMax: time.Second,
		RespectRetryAfter: true, MaxRetryAfter: time.Minute,
	}}
	// A server delay shorter than the backoff still wins.
	if d, ok := p.retryDelay(&APIError{Status: 429, RetryAfter: 100 * time.Millisecond}, 0); !ok || d != 100*time.Millisecond {
		t.Fatalf("short Retry-After: got %v %v", d, ok)
	}
	// One longer than the cap falls back to backoff.
	d, ok := p.retryDelay(&APIError{Status: 429, RetryAfter: time.Hour}, 0)
	if !ok || d > time.Second {
		t.Fatalf("over-cap Retry-After not replaced by backoff: %v %v", d, ok)
	}
	// Ignored entirely when the policy says so.
	p.retry.RespectRetryAfter = false
	if d, _ := p.retryDelay(&APIError{Status: 429, RetryAfter: 5 * time.Second}, 0); d > time.Second {
		t.Fatalf("Retry-After honoured despite RespectRetryAfter=false: %v", d)
	}
}

func TestRetryableStatuses(t *testing.T) {
	r := DefaultRetry
	for _, status := range []int{408, 429, 500, 502, 503, 504, 529, 599} {
		if !r.retryableStatus(status) {
			t.Errorf("status %d should be retryable", status)
		}
	}
	for _, status := range []int{400, 401, 403, 404, 422, 499} {
		if r.retryableStatus(status) {
			t.Errorf("status %d should not be retryable", status)
		}
	}
	custom := Retry{Statuses: []int{503}}
	if custom.retryableStatus(429) || !custom.retryableStatus(503) {
		t.Error("explicit Statuses should replace the default set")
	}
}

func TestBackoffIsCappedAndJittered(t *testing.T) {
	r := DefaultRetry
	for attempt := 0; attempt < 10; attempt++ {
		d := r.backoff(attempt)
		ceiling := min(r.BackoffInitial<<attempt, r.BackoffMax)
		if d > ceiling || d < time.Duration(float64(ceiling)*(1-r.BackoffJitter)) {
			t.Fatalf("attempt %d: backoff %v outside [%v, %v]", attempt, d,
				time.Duration(float64(ceiling)*(1-r.BackoffJitter)), ceiling)
		}
	}
	if d := (Retry{}).backoff(0); d != 0 {
		t.Fatalf("zero policy backoff = %v", d)
	}
}
