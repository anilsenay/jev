package jev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Batch collects questions about one state and evaluates them in a single
// request. Build it with [Client.Batch], add questions with [Add], then call
// [Batch.Run] once. A Batch is not safe for concurrent use.
type Batch struct {
	client   *Client
	state    any
	model    string
	specs    map[string]Spec
	decoders []func(map[string]RawAnswer) error
	buildErr error // invalid questions, reported by Run
	evalErr  error // provider failure, reported by every handle
	ran      bool
}

// Meta describes the request a Batch made.
//
// When the response came from a cache, Model, Usage and RequestID are those of
// the original request; only Latency describes this call.
type Meta struct {
	Model string
	Usage Usage
	// RequestID is what TypeSafe support will ask for. It is empty when the
	// provider reports none.
	RequestID string
	Latency   time.Duration
}

// Batch opens a batch of questions about one state. The state may be plain
// text or a value that encodes as JSON; give its parts names and a question can
// point at the one it is about.
func (c *Client) Batch(state any) *Batch {
	return &Batch{client: c, state: state, specs: make(map[string]Spec)}
}

// UseModel overrides the client's model for this batch.
func (b *Batch) UseModel(model string) *Batch {
	b.model = model
	return b
}

// Len reports how many questions have been added.
func (b *Batch) Len() int { return len(b.decoders) }

// Handle is a typed slot for one answer in a [Batch].
type Handle[A any] struct {
	batch *Batch
	value A
	err   error
}

// Add appends q to b and returns a handle for its answer. An invalid question
// does not panic: its error is returned by [Batch.Run] and by [Handle.Get].
func Add[A any](b *Batch, q Question[A]) *Handle[A] {
	h := &Handle[A]{batch: b}
	if b.ran {
		h.err = ErrBatchUsed
		return h
	}
	id := "q" + strconv.Itoa(len(b.decoders))
	spec, err := q.spec()
	if err != nil {
		h.err = err
		b.buildErr = errors.Join(b.buildErr, err)
		b.decoders = append(b.decoders, func(map[string]RawAnswer) error { return nil })
		return h
	}
	b.specs[id] = spec
	b.decoders = append(b.decoders, func(answers map[string]RawAnswer) error {
		raw, ok := answers[id]
		if !ok {
			h.err = malformed("no answer for %q", spec.Instructions)
			return h.err
		}
		v, err := q.decode(raw)
		if err != nil {
			h.err = fmt.Errorf("question %q: %w", spec.Instructions, err)
			return h.err
		}
		h.value = v
		return nil
	})
	return h
}

// Run sends the batch. It returns an error when a question was invalid, the
// provider failed, or any answer did not fit its question; in the last case
// the handles whose answers were fine can still be read.
func (b *Batch) Run(ctx context.Context) (Meta, error) {
	if b.ran {
		return Meta{}, ErrBatchUsed
	}
	b.ran = true
	if b.buildErr != nil {
		b.evalErr = b.buildErr
		return Meta{}, b.buildErr
	}
	if len(b.specs) == 0 {
		b.evalErr = invalid("batch has no questions")
		return Meta{}, b.evalErr
	}
	state, err := json.Marshal(b.state)
	if err != nil {
		b.evalErr = fmt.Errorf("jev: encoding state: %w", err)
		return Meta{}, b.evalErr
	}
	model := b.model
	if model == "" {
		model = b.client.model
	}

	start := time.Now()
	resp, err := b.client.provider.Evaluate(ctx, &Request{Model: model, State: state, Questions: b.specs})
	latency := time.Since(start)
	if err == nil && resp == nil {
		err = malformed("provider returned no response")
	}
	if err != nil {
		b.evalErr = err
		return Meta{}, err
	}

	var errs []error
	for _, decode := range b.decoders {
		if err := decode(resp.Answers); err != nil {
			errs = append(errs, err)
		}
	}
	meta := Meta{Model: resp.Model, Usage: resp.Usage, RequestID: resp.RequestID, Latency: latency}
	return meta, errors.Join(errs...)
}

// Get returns the typed answer. It returns [ErrNotRun] before the batch ran.
func (h *Handle[A]) Get() (A, error) {
	var zero A
	switch {
	case h.err != nil:
		return zero, h.err
	case !h.batch.ran:
		return zero, ErrNotRun
	case h.batch.evalErr != nil:
		return zero, h.batch.evalErr
	}
	return h.value, nil
}

// Ask evaluates a single question about state. For several independent
// questions about the same state, use a [Batch]: it costs one request.
func Ask[A any](ctx context.Context, c *Client, state any, q Question[A]) (A, error) {
	b := c.Batch(state)
	h := Add(b, q)
	if _, err := b.Run(ctx); err != nil {
		var zero A
		return zero, err
	}
	return h.Get()
}
