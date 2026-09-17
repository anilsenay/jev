// Package jevtest provides a rule-based fake [jev.Provider] for tests.
//
//	fake := jevtest.New().
//		On(jevtest.Instructions("spam"), jevtest.Yes(0.97)).
//		On(jevtest.Instructions("customer want"), jevtest.Pick("refund", 0.9))
//	client, _ := jev.New(jev.WithProvider(fake))
//
// A question that matches no rule fails the request, so a missing rule shows up
// as a test failure instead of a silent default.
package jevtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/anilsenay/jev"
)

// Matcher selects which questions a rule answers.
type Matcher func(state json.RawMessage, q jev.Spec) bool

// Answer produces the raw answer for a matched question.
type Answer func(q jev.Spec) (jev.RawAnswer, error)

type rule struct {
	match  Matcher
	answer Answer
}

// Fake is a [jev.Provider] that answers from rules. It is safe for concurrent use.
type Fake struct {
	mu    sync.Mutex
	rules []rule
	calls []jev.Request
}

// New returns a Fake with no rules.
func New() *Fake { return &Fake{} }

// On adds a rule. Rules are tried in the order they were added.
func (f *Fake) On(m Matcher, a Answer) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, rule{m, a})
	return f
}

// Calls returns the requests received so far. The questions of each call are
// copied, so a test can inspect them while the fake keeps serving.
func (f *Fake) Calls() []jev.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]jev.Request, len(f.calls))
	for i, call := range f.calls {
		questions := make(map[string]jev.Spec, len(call.Questions))
		for id, spec := range call.Questions {
			questions[id] = spec
		}
		call.Questions = questions
		out[i] = call
	}
	return out
}

// Evaluate implements [jev.Provider].
func (f *Fake) Evaluate(ctx context.Context, req *jev.Request) (*jev.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.calls = append(f.calls, *req)
	rules := append([]rule(nil), f.rules...)
	f.mu.Unlock()

	answers := make(map[string]jev.RawAnswer, len(req.Questions))
	for id, q := range req.Questions {
		matched := false
		for _, r := range rules {
			if !r.match(req.State, q) {
				continue
			}
			a, err := r.answer(q)
			if err != nil {
				return nil, fmt.Errorf("jevtest: question %q: %w", jev.Text(q.Instructions), err)
			}
			answers[id] = a
			matched = true
			break
		}
		if !matched {
			return nil, fmt.Errorf("jevtest: no rule matches question %q", jev.Text(q.Instructions))
		}
	}
	return &jev.Response{Model: "jevtest", Answers: answers, RequestID: "req_jevtest"}, nil
}

// Any matches every question.
func Any() Matcher { return func(json.RawMessage, jev.Spec) bool { return true } }

// Instructions matches questions whose instructions contain substr. Structured
// instructions are rendered with [jev.Text] first, so the substring is matched
// against their JSON.
func Instructions(substr string) Matcher {
	return func(_ json.RawMessage, q jev.Spec) bool {
		return strings.Contains(jev.Text(q.Instructions), substr)
	}
}

// Kind matches questions of one kind. It is the reliable matcher when
// instructions are structured.
func Kind(k jev.Kind) Matcher {
	return func(_ json.RawMessage, q jev.Spec) bool { return q.Type == k }
}

// Option matches choice questions that offer the given option.
func Option(name string) Matcher {
	return func(_ json.RawMessage, q jev.Spec) bool {
		for _, o := range q.ChoiceOptions() {
			if o == name {
				return true
			}
		}
		return false
	}
}

// State matches requests whose JSON-encoded state contains substr.
func State(substr string) Matcher {
	return func(s json.RawMessage, _ jev.Spec) bool { return strings.Contains(string(s), substr) }
}

// All matches when every matcher matches.
func All(ms ...Matcher) Matcher {
	return func(s json.RawMessage, q jev.Spec) bool {
		for _, m := range ms {
			if !m(s, q) {
				return false
			}
		}
		return true
	}
}

// Not inverts a matcher.
func Not(m Matcher) Matcher {
	return func(s json.RawMessage, q jev.Spec) bool { return !m(s, q) }
}

// Yes answers a noul question with probability p of yes.
func Yes(p float64) Answer {
	return func(q jev.Spec) (jev.RawAnswer, error) {
		if q.Type != jev.KindNoul {
			return jev.RawAnswer{}, fmt.Errorf("Yes used on a %s question", q.Type)
		}
		return jev.RawAnswer{Type: jev.KindNoul, Noul: &p}, nil
	}
}

// Pick answers a choice with option at probability p and spreads the rest
// evenly over the other options.
func Pick(option string, p float64) Answer {
	return func(q jev.Spec) (jev.RawAnswer, error) {
		if q.Type != jev.KindChoice {
			return jev.RawAnswer{}, fmt.Errorf("Pick used on a %s question", q.Type)
		}
		opts := q.ChoiceOptions()
		probs := make(map[string]float64, len(opts))
		found := false
		for _, o := range opts {
			switch {
			case o == option:
				found = true
				probs[o] = p
			case len(opts) > 1:
				probs[o] = (1 - p) / float64(len(opts)-1)
			default:
				probs[o] = 0
			}
		}
		if !found {
			return jev.RawAnswer{}, fmt.Errorf("Pick(%q): not an option of %v", option, opts)
		}
		conf := p
		return jev.RawAnswer{Type: jev.KindChoice, Choice: option, Probabilities: probs, Confidence: &conf}, nil
	}
}

// Level answers a score with level i at probability p and spreads the rest
// evenly over the other levels. The score value is the weighted mean.
func Level(i int, p float64) Answer {
	return func(q jev.Spec) (jev.RawAnswer, error) {
		if q.Type != jev.KindScore {
			return jev.RawAnswer{}, fmt.Errorf("Level used on a %s question", q.Type)
		}
		n := q.ScoreLevels()
		if i < 0 || i >= n {
			return jev.RawAnswer{}, fmt.Errorf("Level(%d): score has %d levels", i, n)
		}
		probs := make(map[string]float64, n)
		legend := make(map[string]json.RawMessage, n)
		value := 0.0
		for l := 0; l < n; l++ {
			pl := (1 - p) / float64(n-1)
			if l == i {
				pl = p
			}
			key := fmt.Sprint(l)
			probs[key] = pl
			legend[key] = json.RawMessage(`"level ` + key + `"`)
			value += float64(l) * pl
		}
		conf := p
		return jev.RawAnswer{
			Type:          jev.KindScore,
			Score:         &value,
			Legend:        legend,
			Probabilities: probs,
			Confidence:    &conf,
		}, nil
	}
}

// Fail makes matched questions fail the whole request with err.
func Fail(err error) Answer {
	if err == nil {
		err = errors.New("jevtest: injected failure")
	}
	return func(jev.Spec) (jev.RawAnswer, error) { return jev.RawAnswer{}, err }
}
