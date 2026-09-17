package jev

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

// Defaults used when an option is not supplied. They match the official
// TypeSafe SDKs.
const (
	DefaultBaseURL = "https://api.typesafe.ai"
	DefaultModel   = "jev-latest"
	DefaultTimeout = 10 * time.Second

	// APIKeyEnv is read when no key is passed with WithAPIKey.
	APIKeyEnv = "TYPESAFE_API_KEY"
	// BaseURLEnv is read when no URL is passed with WithBaseURL.
	BaseURLEnv = "TYPESAFE_BASE_URL"
	// DefaultModelEnv is read when no model is passed with WithModel.
	DefaultModelEnv = "TYPESAFE_DEFAULT_MODEL"
)

// Client evaluates questions through a [Provider]. It is safe for concurrent use.
type Client struct {
	provider Provider
	lister   ModelLister
	model    string
}

type config struct {
	apiKey     string
	baseURL    string
	model      string
	userAgent  string
	headers    map[string]string
	httpClient *http.Client
	timeout    time.Duration
	retry      Retry
	logger     *slog.Logger
	provider   Provider
	middleware []Middleware
}

// Option configures a [Client].
type Option func(*config)

// WithAPIKey sets the API key, overriding TYPESAFE_API_KEY.
func WithAPIKey(key string) Option { return func(c *config) { c.apiKey = key } }

// WithBaseURL points the HTTP provider at another host, for proxies and tests.
// It overrides TYPESAFE_BASE_URL.
func WithBaseURL(url string) Option {
	return func(c *config) { c.baseURL = strings.TrimRight(url, "/") }
}

// WithModel sets the default model, overriding TYPESAFE_DEFAULT_MODEL.
// [Batch.UseModel] overrides it per batch.
func WithModel(model string) Option { return func(c *config) { c.model = model } }

// WithHTTPClient supplies the http.Client used for requests. The client is
// never modified; per-attempt timeouts are applied through the request context.
func WithHTTPClient(hc *http.Client) Option { return func(c *config) { c.httpClient = hc } }

// WithTimeout bounds each HTTP attempt. Zero disables the per-attempt bound;
// the caller's context still applies.
func WithTimeout(d time.Duration) Option { return func(c *config) { c.timeout = d } }

// WithRetry replaces the retry policy. See [DefaultRetry].
func WithRetry(r Retry) Option { return func(c *config) { c.retry = r } }

// WithRetries sets how many times a retryable failure is retried, leaving the
// rest of the policy alone. Zero disables retries.
func WithRetries(n int) Option { return func(c *config) { c.retry.MaxRetries = max(n, 0) } }

// WithUserAgent sets the User-Agent header.
func WithUserAgent(ua string) Option { return func(c *config) { c.userAgent = ua } }

// WithHeaders adds headers to every request. Authentication, content type and
// the SDK headers cannot be overridden.
func WithHeaders(h map[string]string) Option {
	return func(c *config) {
		if c.headers == nil {
			c.headers = make(map[string]string, len(h))
		}
		for k, v := range h {
			c.headers[k] = v
		}
	}
}

// WithLogger logs one line per request at info level, and retries as they
// happen. Nothing is logged without it.
func WithLogger(l *slog.Logger) Option { return func(c *config) { c.logger = l } }

// WithProvider replaces the HTTP provider, for fakes or alternative backends.
// No API key is required when a provider is supplied.
func WithProvider(p Provider) Option { return func(c *config) { c.provider = p } }

// WithMiddleware wraps the provider. The first middleware given is outermost.
func WithMiddleware(m ...Middleware) Option {
	return func(c *config) { c.middleware = append(c.middleware, m...) }
}

// WithCache caches responses for identical requests. See [NewMemoryCache].
func WithCache(cache Cache) Option { return WithMiddleware(CacheMiddleware(cache)) }

// New builds a client. Without [WithProvider] it talks to the TypeSafe API and
// returns [ErrNoAPIKey] when no key is available.
//
// Explicit options win over environment variables, which win over the defaults.
func New(opts ...Option) (*Client, error) {
	cfg := config{
		userAgent: "jev-go/" + Version,
		timeout:   DefaultTimeout,
		retry:     DefaultRetry,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.baseURL == "" {
		cfg.baseURL = strings.TrimRight(envOr(BaseURLEnv, DefaultBaseURL), "/")
	}
	if cfg.model == "" {
		cfg.model = envOr(DefaultModelEnv, DefaultModel)
	}

	base := cfg.provider
	if base == nil {
		key := cfg.apiKey
		if key == "" {
			key = os.Getenv(APIKeyEnv)
		}
		if strings.TrimSpace(key) == "" {
			return nil, ErrNoAPIKey
		}
		hc := cfg.httpClient
		if hc == nil {
			hc = &http.Client{}
		}
		base = &httpProvider{
			baseURL:   cfg.baseURL,
			apiKey:    strings.TrimSpace(key),
			userAgent: cfg.userAgent,
			headers:   cfg.headers,
			hc:        hc,
			timeout:   cfg.timeout,
			retry:     cfg.retry,
			logger:    cfg.logger,
		}
	}

	// Captured before wrapping, so middleware cannot hide the models endpoint.
	lister, _ := base.(ModelLister)

	p := base
	for i := len(cfg.middleware) - 1; i >= 0; i-- {
		p = cfg.middleware[i](p)
	}
	return &Client{provider: p, lister: lister, model: cfg.model}, nil
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

// Model reports the client's default model.
func (c *Client) Model() string { return c.model }

// Models lists the models the account may use, through GET /v1/models.
//
// It needs a provider that implements [ModelLister], which the default HTTP
// provider does; otherwise it returns [ErrNoModelList]. Middleware does not
// hide it: the underlying provider is asked directly, so cached or instrumented
// clients still list models.
func (c *Client) Models(ctx context.Context) ([]ModelCard, error) {
	if c.lister == nil {
		return nil, ErrNoModelList
	}
	return c.lister.Models(ctx)
}
