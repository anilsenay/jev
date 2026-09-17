package jev

import (
	"context"
	"encoding/json"
	"sort"
	"time"
)

// Kind is the wire type of a question and of its answer.
type Kind string

// The question kinds the System One API accepts.
const (
	KindNoul   Kind = "noul"
	KindChoice Kind = "choice"
	KindScore  Kind = "score"
)

// Content is anything that may be sent as instructions, as a choice option's
// description, as a score level or as a noul criterion: a plain string, or any
// value that marshals to a JSON object or array.
//
// Structure is not decoration. A question with several parts reads more clearly
// as an object with named keys, and a taxonomy or a schema is already JSON, so
// it can be passed through instead of flattened into a sentence. A nil Content
// is sent as JSON null, which the API reads as "no description".
type Content any

// Spec is one question as it is sent over the wire. Most users never build a
// Spec directly; the typed constructors [Noul], [Choice], [OneOf] and [Score]
// produce them. Providers and fakes read them.
type Spec struct {
	Type Kind `json:"type"`
	// Instructions is optional, as it is in the official SDKs: a choice or a
	// score whose criteria say everything can leave it nil, and it is then
	// omitted from the request.
	Instructions Content `json:"instructions,omitempty"`
	// Criteria is *noulCriteria for noul questions, map[string]Content for
	// choices (a nil description is sent as null) and []Content for scores.
	Criteria any `json:"criteria,omitempty"`
}

// ChoiceOptions returns the option names of a choice, sorted. It returns nil for
// other kinds.
func (s Spec) ChoiceOptions() []string {
	c, ok := s.Criteria.(map[string]Content)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(c))
	for k := range c {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ScoreLevels reports how many levels a score has. It returns 0 for other kinds.
func (s Spec) ScoreLevels() int {
	if c, ok := s.Criteria.([]Content); ok {
		return len(c)
	}
	return 0
}

// Request is one evaluation: a state and a battery of questions keyed by id.
type Request struct {
	// Model is required by the API. [Batch.Run] fills it from the client.
	Model     string          `json:"model"`
	State     json.RawMessage `json:"state"`
	Questions map[string]Spec `json:"questions"`
}

// RawAnswer is an answer as it arrives over the wire, before it is narrowed to
// the type of its question.
type RawAnswer struct {
	Type   Kind     `json:"type"`
	Noul   *float64 `json:"noul,omitempty"`
	Choice string   `json:"choice,omitempty"`
	Score  *float64 `json:"score,omitempty"`
	// Legend maps each score level index to the description that was sent. Its
	// values are raw JSON because a level may be structured [Content].
	Legend        map[string]json.RawMessage `json:"legend,omitempty"`
	Probabilities map[string]float64         `json:"probabilities,omitempty"`
	Confidence    *float64                   `json:"confidence,omitempty"`
}

// Usage counts the tokens a request consumed.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Response is the result of one evaluation.
type Response struct {
	Model   string               `json:"model"`
	Answers map[string]RawAnswer `json:"answers"`
	Usage   Usage                `json:"usage"`
	// RequestID identifies the request in TypeSafe's logs. It is read from the
	// response headers rather than the body, so it is not part of the JSON.
	RequestID string `json:"-"`
}

// ModelCard describes one model the account may use.
type ModelCard struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	ReleaseDate time.Time `json:"release_date"`
}

// Provider evaluates requests. The default provider talks to the TypeSafe HTTP
// API; tests use [github.com/anilsenay/jev/jevtest]. Implementations must be
// safe for concurrent use.
type Provider interface {
	Evaluate(ctx context.Context, req *Request) (*Response, error)
}

// ModelLister is the optional half of [Provider], implemented by providers that
// can also list models. [Client.Models] needs it. Middleware that wraps a
// provider hides it unless the wrapper implements it too.
type ModelLister interface {
	Models(ctx context.Context) ([]ModelCard, error)
}

// ProviderFunc adapts a function to [Provider].
type ProviderFunc func(ctx context.Context, req *Request) (*Response, error)

// Evaluate implements [Provider].
func (f ProviderFunc) Evaluate(ctx context.Context, req *Request) (*Response, error) {
	return f(ctx, req)
}

// Middleware wraps a [Provider], for caching, logging, metrics or rate limiting.
type Middleware func(next Provider) Provider
