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

// instructions are optional in both official SDKs: a choice whose criteria say
// everything can leave them out, and the API accepts that.
func TestInstructionsAreOptional(t *testing.T) {
	var got []byte
	c := newClient(t, jev.ProviderFunc(func(_ context.Context, r *jev.Request) (*jev.Response, error) {
		got, _ = json.Marshal(r)
		return nil, errors.New("stop")
	}), jev.WithModel("jev-latest"))

	b := c.Batch("I want a refund")
	jev.Add(b, jev.Choice[Intent](nil, jev.Opt(Refund, "asks for money back"), jev.Opt(Other, nil)))
	_, _ = b.Run(context.Background())

	want := `{"model":"jev-latest","state":"I want a refund","questions":{` +
		`"q0":{"type":"choice","criteria":{"other":null,"refund":"asks for money back"}}}}`
	if string(got) != want {
		t.Fatalf("wire format\n got: %s\nwant: %s", got, want)
	}
}

// The status-to-error mapping matches the official SDKs' exception hierarchy.
func TestErrorTaxonomy(t *testing.T) {
	cases := []struct {
		status    int
		is        []error
		isNot     []error
		retryable bool
	}{
		{400, []error{jev.ErrBadRequest, jev.ErrInvalidRequest}, []error{jev.ErrAuth, jev.ErrUnprocessable}, false},
		{401, []error{jev.ErrAuth}, []error{jev.ErrPermissionDenied, jev.ErrInvalidRequest}, false},
		{403, []error{jev.ErrPermissionDenied}, []error{jev.ErrAuth}, false},
		{404, []error{jev.ErrNotFound}, []error{jev.ErrInvalidRequest}, false},
		{408, nil, []error{jev.ErrInternalServer}, true},
		{422, []error{jev.ErrUnprocessable, jev.ErrInvalidRequest}, []error{jev.ErrBadRequest}, false},
		{429, []error{jev.ErrRateLimit}, []error{jev.ErrInternalServer}, true},
		{500, []error{jev.ErrInternalServer}, []error{jev.ErrRateLimit}, true},
		{503, []error{jev.ErrInternalServer}, nil, true},
		{jev.StatusOverloaded, []error{jev.ErrOverloaded, jev.ErrInternalServer}, nil, true},
		{599, []error{jev.ErrInternalServer}, nil, true},
	}
	for _, tc := range cases {
		err := &jev.APIError{Status: tc.status}
		for _, target := range tc.is {
			if !errors.Is(err, target) {
				t.Errorf("status %d should match %v", tc.status, target)
			}
		}
		for _, target := range tc.isNot {
			if errors.Is(err, target) {
				t.Errorf("status %d should not match %v", tc.status, target)
			}
		}
		if err.Retryable() != tc.retryable {
			t.Errorf("status %d Retryable = %v, want %v", tc.status, err.Retryable(), tc.retryable)
		}
	}
}

// Message extraction follows the official order: error, then message, then detail.
func TestErrorMessagePrecedence(t *testing.T) {
	cases := map[string]struct {
		body     string
		wantType string
		wantMsg  string
	}{
		"error string": {`{"error":"boom","message":"ignored"}`, "", "boom"},
		"error object": {`{"error":{"message":"deep"},"detail":"ignored"}`, "", "deep"},
		"message":      {`{"message":"plain","detail":"ignored"}`, "", "plain"},
		"detail typed": {`{"detail":{"error_type":"api_usage_error","message":"Unknown model"}}`, "api_usage_error", "Unknown model"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := server(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, tc.body)
			})
			_, err := jev.Ask(context.Background(), httpClient(t, s.URL), "x", spamQ)
			var apiErr *jev.APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("err = %v", err)
			}
			if apiErr.Type != tc.wantType || apiErr.Message != tc.wantMsg {
				t.Fatalf("type=%q message=%q", apiErr.Type, apiErr.Message)
			}
		})
	}
}

// Every request carries the SDK identification headers the official clients send.
func TestSDKHeaders(t *testing.T) {
	var attempts atomic.Int32
	var last http.Header
	s := server(t, func(w http.ResponseWriter, r *http.Request) {
		last = r.Header.Clone()
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, okBody)
	})
	c := httpClient(t, s.URL,
		jev.WithRetry(jev.Retry{MaxRetries: 2, RetryConnection: true, RetryTimeout: true}),
		jev.WithHeaders(map[string]string{"X-Tenant": "acme", "Authorization": "cannot override"}),
	)
	if _, err := jev.Ask(context.Background(), c, "x", spamQ); err != nil {
		t.Fatal(err)
	}
	for header, want := range map[string]string{
		"User-Agent":             "jev-go/" + jev.Version,
		"X-Typesafe-Sdk":         "jev-go/" + jev.Version,
		"X-Typesafe-Retry-Count": "1",
		"X-Tenant":               "acme",
		"Authorization":          "Bearer k",
		"Content-Type":           "application/json",
		"Accept":                 "application/json",
	} {
		if got := last.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if last.Get("X-Typesafe-Runtime") == "" {
		t.Error("X-TypeSafe-Runtime is missing")
	}
}

func TestRetryCountHeaderAbsentOnFirstAttempt(t *testing.T) {
	var first http.Header
	s := server(t, func(w http.ResponseWriter, r *http.Request) {
		if first == nil {
			first = r.Header.Clone()
		}
		_, _ = io.WriteString(w, okBody)
	})
	if _, err := jev.Ask(context.Background(), httpClient(t, s.URL), "x", spamQ); err != nil {
		t.Fatal(err)
	}
	if _, ok := first["X-Typesafe-Retry-Count"]; ok {
		t.Error("retry count header sent on the first attempt")
	}
}

// Base URL and default model fall back to the environment, as they do in the
// official SDKs; explicit options still win.
func TestEnvironmentFallbacks(t *testing.T) {
	var path string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = io.WriteString(w, okBody)
	}))
	t.Cleanup(s.Close)

	t.Setenv(jev.APIKeyEnv, "from-env")
	t.Setenv(jev.BaseURLEnv, s.URL+"/")
	t.Setenv(jev.DefaultModelEnv, "jev-preview")

	c, err := jev.New()
	if err != nil {
		t.Fatal(err)
	}
	if c.Model() != "jev-preview" {
		t.Fatalf("model = %q", c.Model())
	}
	if _, err := jev.Ask(context.Background(), c, "x", spamQ); err != nil {
		t.Fatal(err)
	}
	if path != "/v1/systemone" {
		t.Fatalf("path = %q (trailing slash not trimmed?)", path)
	}

	explicit, err := jev.New(jev.WithModel("jev-latest"))
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Model() != "jev-latest" {
		t.Fatalf("explicit model lost: %q", explicit.Model())
	}
}

// A connection failure and a timeout are distinguishable, as APIConnectionError
// and APITimeoutError are in the official SDKs.
func TestConnectionAndTimeoutErrors(t *testing.T) {
	t.Run("connection", func(t *testing.T) {
		s := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := s.URL
		s.Close() // nothing is listening any more
		_, err := jev.Ask(context.Background(), httpClient(t, url, jev.WithRetries(0)), "x", spamQ)
		if !errors.Is(err, jev.ErrConnection) || errors.Is(err, jev.ErrTimeout) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		s := server(t, func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(200 * time.Millisecond)
			_, _ = io.WriteString(w, okBody)
		})
		c := httpClient(t, s.URL, jev.WithRetries(0), jev.WithTimeout(20*time.Millisecond))
		_, err := jev.Ask(context.Background(), c, "x", spamQ)
		if !errors.Is(err, jev.ErrTimeout) || !errors.Is(err, jev.ErrConnection) {
			t.Fatalf("err = %v", err)
		}
	})
}

// Middleware no longer hides the models endpoint.
func TestModelsWorksThroughMiddleware(t *testing.T) {
	s := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"models":[{"name":"jev-latest","description":"d","release_date":"2026-09-10T18:38:01Z"}]}`)
	})
	c := httpClient(t, s.URL, jev.WithCache(jev.NewMemoryCache(4)))
	models, err := c.Models(context.Background())
	if err != nil || len(models) != 1 {
		t.Fatalf("models = %v, err = %v", models, err)
	}
}

func TestDefaultsMatchOfficialSDKs(t *testing.T) {
	if jev.DefaultTimeout != 10*time.Second {
		t.Errorf("DefaultTimeout = %v, want 10s", jev.DefaultTimeout)
	}
	if jev.DefaultRetry.MaxRetries != 2 ||
		jev.DefaultRetry.BackoffInitial != 500*time.Millisecond ||
		jev.DefaultRetry.BackoffMax != 5*time.Second ||
		jev.DefaultRetry.BackoffJitter != 0.25 ||
		jev.DefaultRetry.MaxRetryAfter != time.Minute ||
		!jev.DefaultRetry.RespectRetryAfter {
		t.Errorf("DefaultRetry = %+v", jev.DefaultRetry)
	}
	if jev.DefaultBaseURL != "https://api.typesafe.ai" || jev.DefaultModel != "jev-latest" {
		t.Errorf("base URL %q model %q", jev.DefaultBaseURL, jev.DefaultModel)
	}
}
