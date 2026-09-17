package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	maxErrorBody = 4 << 10

	pathEvaluate = "/v1/systemone"
	pathModels   = "/v1/models"

	// Headers the official SDKs send and read. requestIDHeader is matched
	// case-insensitively by http.Header.Get.
	requestIDHeader       = "X-Typesafe-Request-Id"
	legacyRequestIDHeader = "X-Request-Id"
	sdkHeader             = "X-TypeSafe-SDK"
	runtimeHeader         = "X-TypeSafe-Runtime"
	retryCountHeader      = "X-TypeSafe-Retry-Count"
	retryAfterHeader      = "Retry-After"
	retryAfterMsHeader    = "Retry-After-Ms"
)

// runtimeToken describes this process, for the X-TypeSafe-Runtime header.
var runtimeToken = fmt.Sprintf("go/%s (%s; %s)",
	strings.TrimPrefix(runtime.Version(), "go"), runtime.GOOS, runtime.GOARCH)

type httpProvider struct {
	baseURL   string
	apiKey    string
	userAgent string
	headers   map[string]string
	hc        *http.Client
	timeout   time.Duration
	retry     Retry
	logger    *slog.Logger
}

// Evaluate implements [Provider].
func (p *httpProvider) Evaluate(ctx context.Context, req *Request) (*Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("jev: encoding request: %w", err)
	}
	var out Response
	requestID, err := p.do(ctx, http.MethodPost, pathEvaluate, body, &out)
	if err != nil {
		return nil, err
	}
	out.RequestID = requestID
	return &out, nil
}

// Models implements [ModelLister].
func (p *httpProvider) Models(ctx context.Context) ([]ModelCard, error) {
	var out struct {
		Models []ModelCard `json:"models"`
	}
	if _, err := p.do(ctx, http.MethodGet, pathModels, nil, &out); err != nil {
		return nil, err
	}
	if out.Models == nil {
		return nil, malformed("unexpected response from GET %s: expected {\"models\": [...]}", pathModels)
	}
	return out.Models, nil
}

// do sends one request, retrying retryable failures, and decodes the body into
// out. It returns the request id the API reported.
func (p *httpProvider) do(ctx context.Context, method, path string, body []byte, out any) (string, error) {
	for attempt := 0; ; attempt++ {
		requestID, err := p.attempt(ctx, method, path, body, attempt, out)
		if err == nil {
			return requestID, nil
		}
		if ctx.Err() != nil {
			return "", fmt.Errorf("jev: %w (last error: %v)", ctx.Err(), err)
		}
		wait, retry := p.retryDelay(err, attempt)
		if !retry || attempt >= p.retry.MaxRetries {
			return "", err
		}
		if p.logger != nil {
			p.logger.Info("jev: retrying", "method", method, "path", path,
				"attempt", attempt+1, "wait", wait, "cause", err)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", fmt.Errorf("jev: %w (last error: %v)", ctx.Err(), err)
		case <-timer.C:
		}
	}
}

func (p *httpProvider) attempt(ctx context.Context, method, path string, body []byte, attempt int, out any) (string, error) {
	limit := p.timeout
	if limit > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, limit)
		defer cancel()
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, reader)
	if err != nil {
		return "", fmt.Errorf("jev: building request: %w", err)
	}
	// Caller headers go first so they cannot clobber auth or the content type.
	for name, value := range p.headers {
		req.Header.Set(name, value)
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", p.userAgent)
	req.Header.Set(sdkHeader, "jev-go/"+Version)
	req.Header.Set(runtimeHeader, runtimeToken)
	req.Header.Del(retryCountHeader)
	if attempt > 0 {
		req.Header.Set(retryCountHeader, strconv.Itoa(attempt))
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	started := time.Now()
	resp, err := p.hc.Do(req)
	if err != nil {
		tErr := &transportError{err: err, isTimeout: errors.Is(err, context.DeadlineExceeded), limit: limit}
		p.log(method, path, 0, "", time.Since(started), tErr)
		return "", tErr
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
	}()

	requestID := requestIDOf(resp.Header)
	endpoint := method + " " + path

	if resp.StatusCode/100 != 2 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		apiErr := &APIError{
			Status:     resp.StatusCode,
			Body:       strings.TrimSpace(string(snippet)),
			Header:     resp.Header,
			RequestID:  requestID,
			RetryAfter: parseRetryAfter(resp.Header, time.Now()),
			Endpoint:   endpoint,
		}
		apiErr.parseErrorBody(snippet)
		p.log(method, path, resp.StatusCode, requestID, time.Since(started), apiErr)
		return requestID, apiErr
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		if ctx.Err() != nil { // body cut off by the per-attempt timeout
			return requestID, &transportError{err: err, isTimeout: true, limit: limit}
		}
		return requestID, malformed("decoding response: %v", err)
	}
	p.log(method, path, resp.StatusCode, requestID, time.Since(started), nil)
	return requestID, nil
}

func (p *httpProvider) log(method, path string, status int, requestID string, took time.Duration, err error) {
	if p.logger == nil {
		return
	}
	attrs := []any{"method", method, "path", path, "took", took}
	if status != 0 {
		attrs = append(attrs, "status", status)
	}
	if requestID != "" {
		attrs = append(attrs, "request_id", requestID)
	}
	if err != nil {
		p.logger.Info("jev: request failed", append(attrs, "error", err)...)
		return
	}
	p.logger.Info("jev: request", attrs...)
}

func requestIDOf(h http.Header) string {
	if id := h.Get(requestIDHeader); id != "" {
		return id
	}
	return h.Get(legacyRequestIDHeader)
}

// retryDelay decides whether err is worth retrying and how long to wait first.
// A server delay within MaxRetryAfter wins; anything longer falls back to
// backoff, as it does in the official SDKs.
func (p *httpProvider) retryDelay(err error, attempt int) (time.Duration, bool) {
	var apiErr *APIError
	var tErr *transportError
	switch {
	case errors.As(err, &apiErr):
		if !p.retry.retryableStatus(apiErr.Status) {
			return 0, false
		}
		if p.retry.RespectRetryAfter && apiErr.RetryAfter > 0 && apiErr.RetryAfter <= p.retry.MaxRetryAfter {
			return apiErr.RetryAfter, true
		}
		return p.retry.backoff(attempt), true
	case errors.As(err, &tErr):
		if tErr.isTimeout {
			return p.retry.backoff(attempt), p.retry.RetryTimeout
		}
		return p.retry.backoff(attempt), p.retry.RetryConnection
	}
	return 0, false
}

// parseRetryAfter reads retry-after-ms first, then Retry-After as seconds or as
// an HTTP date. It returns zero when neither carries a usable delay.
func parseRetryAfter(h http.Header, now time.Time) time.Duration {
	if raw := strings.TrimSpace(h.Get(retryAfterMsHeader)); raw != "" {
		if ms, err := strconv.ParseFloat(raw, 64); err == nil && !math.IsInf(ms, 0) && !math.IsNaN(ms) && ms >= 0 {
			return time.Duration(ms * float64(time.Millisecond))
		}
	}
	raw := strings.TrimSpace(h.Get(retryAfterHeader))
	if raw == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(raw, 64); err == nil {
		if math.IsInf(secs, 0) || math.IsNaN(secs) || secs < 0 {
			return 0
		}
		return time.Duration(secs * float64(time.Second))
	}
	if t, err := http.ParseTime(raw); err == nil {
		return max(t.Sub(now), 0)
	}
	return 0
}
