package jev

import (
	"math/rand/v2"
	"time"
)

// Retry configures how a failed request is retried. Its zero value is not
// useful; start from [DefaultRetry] and change what you need.
//
// The defaults mirror the official TypeSafe SDKs: two retries, 500ms of initial
// backoff doubling to 5s with 25% jitter subtracted, honouring a server delay of
// up to a minute, retrying 408, 429 and every 5xx.
type Retry struct {
	// MaxRetries is how many attempts follow the first. Zero disables retries.
	MaxRetries int
	// BackoffInitial is the first delay, doubled on each attempt up to
	// BackoffMax. Zero disables backoff.
	BackoffInitial time.Duration
	// BackoffMax caps a single backoff delay.
	BackoffMax time.Duration
	// BackoffJitter is the fraction of each delay randomly subtracted, from 0
	// to 1. It stops parallel callers from retrying in lockstep.
	BackoffJitter float64
	// Statuses lists the HTTP status codes to retry. Nil means the default:
	// 408, 429 and every 5xx.
	Statuses []int
	// RespectRetryAfter honours the retry-after-ms and Retry-After headers.
	RespectRetryAfter bool
	// MaxRetryAfter is the longest server delay that is honoured. A longer one
	// falls back to backoff.
	MaxRetryAfter time.Duration
	// RetryConnection retries failures that produced no HTTP response.
	RetryConnection bool
	// RetryTimeout retries attempts that ran out of time.
	RetryTimeout bool
}

// DefaultRetry is the policy used when none is supplied.
var DefaultRetry = Retry{
	MaxRetries:        2,
	BackoffInitial:    500 * time.Millisecond,
	BackoffMax:        5 * time.Second,
	BackoffJitter:     0.25,
	RespectRetryAfter: true,
	MaxRetryAfter:     time.Minute,
	RetryConnection:   true,
	RetryTimeout:      true,
}

// retryableStatus reports whether status is one the policy retries.
func (r Retry) retryableStatus(status int) bool {
	if r.Statuses == nil {
		return status == 408 || status == 429 || (status >= 500 && status <= 599)
	}
	for _, s := range r.Statuses {
		if s == status {
			return true
		}
	}
	return false
}

// backoff returns the delay before the retry following a zero-based attempt:
// exponential, capped, with a random fraction subtracted.
func (r Retry) backoff(attempt int) time.Duration {
	if r.BackoffInitial <= 0 || r.BackoffMax <= 0 {
		return 0
	}
	d := r.BackoffMax
	if attempt < 30 {
		d = min(r.BackoffInitial<<attempt, r.BackoffMax)
	}
	if r.BackoffJitter <= 0 {
		return d
	}
	return time.Duration(float64(d) * (1 - rand.Float64()*min(r.BackoffJitter, 1)))
}
