package jev_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anilsenay/jev"
)

const okBody = `{"model":"jev-1.13.0","answers":{"q0":{"type":"noul","noul":0.91}},"usage":{"input_tokens":42,"output_tokens":1}}`

func server(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func httpClient(t *testing.T, url string, opts ...jev.Option) *jev.Client {
	t.Helper()
	c, err := jev.New(append([]jev.Option{jev.WithAPIKey("k"), jev.WithBaseURL(url)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestHTTPRequestAndResponse(t *testing.T) {
	s := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" || r.Method != http.MethodPost {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("auth header %q", r.Header.Get("Authorization"))
		}
		var req jev.Request
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil || req.Model != jev.DefaultModel || string(req.State) != `"hello"` {
			t.Errorf("request %s (%v)", body, err)
		}
		_, _ = io.WriteString(w, okBody)
	})
	c := httpClient(t, s.URL)
	b := c.Batch("hello")
	h := jev.Add(b, spamQ)
	meta, err := b.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a, _ := h.Get()
	if a.P != 0.91 || meta.Model != "jev-1.13.0" || meta.Usage.InputTokens != 42 {
		t.Fatalf("answer %v meta %+v", a, meta)
	}
}

func TestHTTPRetriesOverloaded(t *testing.T) {
	var calls atomic.Int32
	s := server(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(jev.StatusOverloaded)
			return
		}
		_, _ = io.WriteString(w, okBody)
	})
	if _, err := jev.Ask(context.Background(), httpClient(t, s.URL), "s", spamQ); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestHTTPDoesNotRetryAuthOrDecodeErrors(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"401":      func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) },
		"bad json": func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "not json") },
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			s := server(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); h(w, r) })
			_, err := jev.Ask(context.Background(), httpClient(t, s.URL), "s", spamQ)
			if err == nil || calls.Load() != 1 {
				t.Fatalf("err %v calls %d", err, calls.Load())
			}
			if name == "401" && !errors.Is(err, jev.ErrAuth) {
				t.Fatalf("want ErrAuth, got %v", err)
			}
			if name == "bad json" && !errors.Is(err, jev.ErrMalformedAnswer) {
				t.Fatalf("want ErrMalformedAnswer, got %v", err)
			}
		})
	}
}

func TestHTTPRetriesPerAttemptTimeout(t *testing.T) {
	var calls atomic.Int32
	s := server(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			select {
			case <-time.After(time.Second):
			case <-r.Context().Done():
			}
			return
		}
		_, _ = io.WriteString(w, okBody)
	})
	c := httpClient(t, s.URL, jev.WithTimeout(50*time.Millisecond))
	if _, err := jev.Ask(context.Background(), c, "s", spamQ); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestHTTPClientIsNotMutated(t *testing.T) {
	hc := &http.Client{Timeout: 5 * time.Second}
	_ = httpClient(t, "http://x", jev.WithHTTPClient(hc), jev.WithTimeout(time.Millisecond))
	_ = httpClient(t, "http://x", jev.WithHTTPClient(nil))
	if hc.Timeout != 5*time.Second {
		t.Fatalf("timeout mutated to %v", hc.Timeout)
	}
}

func TestHTTPContextCancelDuringBackoff(t *testing.T) {
	s := server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := jev.Ask(ctx, httpClient(t, s.URL), "s", spamQ)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatalf("err %v after %v", err, time.Since(start))
	}
}

func TestHTTPGivesUpAfterRetries(t *testing.T) {
	var calls atomic.Int32
	s := server(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) })
	_, err := jev.Ask(context.Background(), httpClient(t, s.URL, jev.WithRetries(1)), "s", spamQ)
	var apiErr *jev.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 503 || calls.Load() != 2 {
		t.Fatalf("err %v calls %d", err, calls.Load())
	}
}
