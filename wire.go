package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

// Content is what fills the writable parts of a question: its instructions, a
// choice option's description, a score level, either side of a noul. Plain text
// covers most of them; anything that encodes as a JSON object or array covers
// the rest.
//
// Structure earns its place when flattening would lose something. A question
// with several parts reads more clearly under named keys than in one sentence,
// and a taxonomy or a schema is already JSON, so it can go through as it
// stands. A nil Content reaches the API as null, meaning "left undescribed".
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
	// Extra carries question fields this package does not model, for API
	// features that land before a release does. Its entries are marshalled
	// alongside the three above, sorted by key; "type", "instructions" and
	// "criteria" are reserved and ignored here. The typed constructors never
	// set it; see [RawQuestion].
	Extra map[string]any `json:"-"`
}

// specWire is Spec without Extra, so MarshalJSON can render the modelled fields
// in their usual order before appending the rest.
type specWire struct {
	Type         Kind    `json:"type"`
	Instructions Content `json:"instructions,omitempty"`
	Criteria     any     `json:"criteria,omitempty"`
}

// MarshalJSON implements [json.Marshaler], merging [Spec.Extra] into the
// question object.
func (s Spec) MarshalJSON() ([]byte, error) {
	base, err := json.Marshal(specWire{s.Type, s.Instructions, s.Criteria})
	if err != nil {
		return nil, err
	}
	if len(s.Extra) == 0 {
		return base, nil
	}
	keys := make([]string, 0, len(s.Extra))
	for k := range s.Extra {
		switch k {
		case "type", "instructions", "criteria": // modelled above; never overridden
		default:
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return base, nil
	}
	sort.Strings(keys)

	var b bytes.Buffer
	b.Write(base[:len(base)-1]) // drop the closing brace
	for _, k := range keys {
		key, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(s.Extra[k])
		if err != nil {
			return nil, fmt.Errorf("jev: encoding extra question field %q: %w", k, err)
		}
		b.WriteByte(',')
		b.Write(key)
		b.WriteByte(':')
		b.Write(value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
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

// Request is a single call: one state, and the questions to ask about it,
// each under an id of the caller's choosing.
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

// Response is what came back for a [Request].
type Response struct {
	Model   string               `json:"model"`
	Answers map[string]RawAnswer `json:"answers"`
	Usage   Usage                `json:"usage"`
	// RequestID is what TypeSafe support will ask for. It travels in a response
	// header rather than the body, which is why it is not part of the JSON.
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
