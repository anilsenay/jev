package jev

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Limits the API enforces on a single question. Like the official SDKs, this
// package does not check them before sending: a question that exceeds one comes
// back as an [APIError] matching [ErrBadRequest]. They are exported so callers
// building option lists programmatically can check for themselves.
const (
	// MaxChoiceOptions is the largest number of options one choice may offer.
	MaxChoiceOptions = 255
	// MaxScoreLevels is the largest number of levels one score may define.
	MaxScoreLevels = 10
)

// Question is a typed question whose answer decodes to A. Build one with
// [Noul], [Choice], [OneOf] or [Score]. Questions are immutable values and are
// meant to be declared once, typically as package-level variables.
//
// The interface is sealed: only this package can implement it, because the API
// accepts only these kinds.
type Question[A any] interface {
	spec() (Spec, error)
	decode(raw RawAnswer) (A, error)
}

// Text renders a [Content] for logs and messages: a string is returned as it
// is, anything else as compact JSON.
func Text(c Content) string {
	switch v := c.(type) {
	case nil:
		return ""
	case string:
		return v
	}
	b, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return string(b)
}

// ---------------------------------------------------------------- noul

// NoulCriteria describes the two ends of a [Noul]. Either side may be left nil,
// and either may be structured [Content] when the boundary is subtle.
type NoulCriteria struct {
	// True is what a value near 1 means.
	True Content
	// False is what a value near 0 means.
	False Content
}

type noulCriteria struct {
	True  Content `json:"true,omitempty"`
	False Content `json:"false,omitempty"`
}

type noulQ struct {
	instructions Content
	criteria     *NoulCriteria
}

// Noul asks whether a condition holds and answers with the probability that it
// does. Pass a [NoulCriteria] to describe the two ends; filling them in usually
// sharpens the boundary. The API needs at least one of instructions and
// criteria.
//
// Use one Noul per label when several labels can apply at once. A value near
// 0.5 means the model finds yes and no about equally likely; it does not mean
// "medium". Use a [Score] to place something on a spectrum.
func Noul(instructions Content, criteria ...NoulCriteria) Question[NoulAnswer] {
	if len(criteria) > 1 {
		// Reported by spec and by Handle.Get, so that building a question never
		// panics and never silently drops an argument.
		return badNoul{n: len(criteria)}
	}
	q := noulQ{instructions: instructions}
	if len(criteria) == 1 {
		c := criteria[0]
		q.criteria = &c
	}
	return q
}

type badNoul struct{ n int }

func (b badNoul) spec() (Spec, error) {
	return Spec{}, invalid("noul takes at most one NoulCriteria, got %d", b.n)
}
func (badNoul) decode(RawAnswer) (NoulAnswer, error) {
	return NoulAnswer{}, invalid("noul takes at most one NoulCriteria")
}

func (q noulQ) spec() (Spec, error) {
	s := Spec{Type: KindNoul, Instructions: q.instructions}
	if q.criteria != nil && (q.criteria.True != nil || q.criteria.False != nil) {
		s.Criteria = &noulCriteria{True: q.criteria.True, False: q.criteria.False}
	}
	return s, nil
}

func (q noulQ) decode(raw RawAnswer) (NoulAnswer, error) {
	if raw.Type != KindNoul {
		return NoulAnswer{}, malformed("expected %s answer, got %q", KindNoul, raw.Type)
	}
	if raw.Noul == nil || !isProb(*raw.Noul) {
		return NoulAnswer{}, malformed("noul answer has no valid probability")
	}
	return NoulAnswer{P: *raw.Noul}, nil
}

// NoulAnswer is the probability that the answer is yes. A noul carries no
// separate confidence: the probability itself is the signal.
type NoulAnswer struct {
	P float64
}

// Sure reports the answer and whether it clears threshold in either direction:
// (true, true) when P >= threshold, (false, true) when 1-P >= threshold, and
// (false, false) otherwise. Use a threshold above 0.5.
func (a NoulAnswer) Sure(threshold float64) (yes, ok bool) {
	switch {
	case a.P >= threshold:
		return true, true
	case 1-a.P >= threshold:
		return false, true
	}
	return false, false
}

// ---------------------------------------------------------------- choice

// ChoiceOption is one choice option with an optional description. The
// description may be a string, structured [Content], or nil.
type ChoiceOption[T ~string] struct {
	Value       T
	Description Content
}

// Opt builds a [ChoiceOption]. T is inferred from value, so passing your own
// string enum makes the whole choice typed. Pass nil as the description when
// the option name speaks for itself.
func Opt[T ~string](value T, description Content) ChoiceOption[T] {
	return ChoiceOption[T]{Value: value, Description: description}
}

type choiceQ[T ~string] struct {
	instructions Content
	options      []ChoiceOption[T]
}

// Choice picks exactly one of options and returns the distribution over them.
// Include a catch-all option when none of the others may fit: the model cannot
// answer outside the set.
func Choice[T ~string](instructions Content, options ...ChoiceOption[T]) Question[ChoiceAnswer[T]] {
	return choiceQ[T]{instructions, append([]ChoiceOption[T](nil), options...)}
}

// OneOf is [Choice] without descriptions, for options whose names speak for
// themselves.
func OneOf[T ~string](instructions Content, values ...T) Question[ChoiceAnswer[T]] {
	opts := make([]ChoiceOption[T], len(values))
	for i, v := range values {
		opts[i] = ChoiceOption[T]{Value: v}
	}
	return choiceQ[T]{instructions, opts}
}

func (q choiceQ[T]) spec() (Spec, error) {
	criteria := make(map[string]Content, len(q.options))
	for _, o := range q.options {
		name := string(o.Value)
		if strings.TrimSpace(name) == "" {
			return Spec{}, invalid("choice %q has an empty option", Text(q.instructions))
		}
		if _, dup := criteria[name]; dup {
			return Spec{}, invalid("choice %q has duplicate option %q", Text(q.instructions), name)
		}
		// A nil description must reach the API as JSON null, not as "".
		if s, ok := o.Description.(string); ok && s == "" {
			criteria[name] = nil
			continue
		}
		criteria[name] = o.Description
	}
	return Spec{Type: KindChoice, Instructions: q.instructions, Criteria: criteria}, nil
}

func (q choiceQ[T]) decode(raw RawAnswer) (ChoiceAnswer[T], error) {
	var zero ChoiceAnswer[T]
	if raw.Type != KindChoice {
		return zero, malformed("expected %s answer, got %q", KindChoice, raw.Type)
	}
	valid := make(map[string]T, len(q.options))
	for _, o := range q.options {
		valid[string(o.Value)] = o.Value
	}
	selected, ok := valid[raw.Choice]
	if !ok {
		return zero, malformed("choice answer %q is not one of the options", raw.Choice)
	}
	if len(raw.Probabilities) == 0 {
		return zero, malformed("choice answer carries no probabilities")
	}
	probs := make(map[T]float64, len(q.options))
	for _, o := range q.options {
		probs[o.Value] = 0
	}
	for name, p := range raw.Probabilities {
		v, ok := valid[name]
		if !ok {
			return zero, malformed("probability for unknown option %q", name)
		}
		if !isProb(p) {
			return zero, malformed("invalid probability %v for option %q", p, name)
		}
		probs[v] = p
	}
	if _, ok := raw.Probabilities[raw.Choice]; !ok {
		return zero, malformed("no probability for the selected option %q", raw.Choice)
	}
	return ChoiceAnswer[T]{Value: selected, Probs: probs, Confidence: deref(raw.Confidence)}, nil
}

// ChoiceAnswer is the selected option and the distribution over all options.
type ChoiceAnswer[T ~string] struct {
	// Value is the most probable option.
	Value T
	// Probs holds a probability for every option, including zero ones.
	Probs map[T]float64
	// Confidence is the API's confidence: how peaked the distribution is, from
	// 0 to 1. It is not a calibrated probability that the answer is right; use
	// Probs[Value] for that.
	Confidence float64
}

// Sure returns Value and whether its probability is at least threshold.
func (a ChoiceAnswer[T]) Sure(threshold float64) (T, bool) {
	return a.Value, a.Probs[a.Value] >= threshold
}

// Ranked returns the options from most to least probable.
func (a ChoiceAnswer[T]) Ranked() []T {
	out := make([]T, 0, len(a.Probs))
	for v := range a.Probs {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if a.Probs[out[i]] != a.Probs[out[j]] {
			return a.Probs[out[i]] > a.Probs[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}

// ---------------------------------------------------------------- score

type scoreQ struct {
	instructions Content
	levels       []Content
}

// Score rates the state against ordered levels, lowest first. Each level should
// describe a concrete situation, as a string or as structured [Content]. The
// answer is a probability-weighted position, so it can land between levels.
func Score(instructions Content, levels ...Content) Question[ScoreAnswer] {
	return scoreQ{instructions, append([]Content(nil), levels...)}
}

func (q scoreQ) spec() (Spec, error) {
	if n := len(q.levels); n < 2 {
		return Spec{}, invalid("score %q has %d criteria; at least two scores are required",
			Text(q.instructions), n)
	}
	return Spec{
		Type:         KindScore,
		Instructions: q.instructions,
		Criteria:     append([]Content(nil), q.levels...),
	}, nil
}

func (q scoreQ) decode(raw RawAnswer) (ScoreAnswer, error) {
	if raw.Type != KindScore {
		return ScoreAnswer{}, malformed("expected %s answer, got %q", KindScore, raw.Type)
	}
	n := len(q.levels)
	if raw.Score == nil || math.IsNaN(*raw.Score) || *raw.Score < 0 || *raw.Score > float64(n-1) {
		return ScoreAnswer{}, malformed("score answer is missing or outside 0..%d", n-1)
	}
	if len(raw.Probabilities) == 0 {
		return ScoreAnswer{}, malformed("score answer carries no probabilities")
	}
	probs := make([]float64, n)
	for key, p := range raw.Probabilities {
		i, err := strconv.Atoi(key)
		if err != nil || i < 0 || i >= n {
			return ScoreAnswer{}, malformed("probability for unknown level %q", key)
		}
		if !isProb(p) {
			return ScoreAnswer{}, malformed("invalid probability %v for level %q", p, key)
		}
		probs[i] = p
	}
	legend, err := decodeLegend(raw.Legend)
	if err != nil {
		return ScoreAnswer{}, err
	}
	return ScoreAnswer{
		Value:      *raw.Score,
		Probs:      probs,
		Levels:     append([]Content(nil), q.levels...),
		Legend:     legend,
		Confidence: deref(raw.Confidence),
	}, nil
}

// decodeLegend turns the wire legend into level index to description. A level
// description may be structured, so each value decodes into a Content.
func decodeLegend(raw map[string]json.RawMessage) (map[int]Content, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[int]Content, len(raw))
	for key, value := range raw {
		i, err := strconv.Atoi(key)
		if err != nil {
			return nil, malformed("legend key %q is not a level index", key)
		}
		var content Content
		if err := json.Unmarshal(value, &content); err != nil {
			return nil, malformed("legend level %d: %v", i, err)
		}
		out[i] = content
	}
	return out, nil
}

// ScoreAnswer is a probability-weighted position across the levels of a Score.
type ScoreAnswer struct {
	// Value runs from 0 (the first level) to len(Levels)-1.
	Value float64
	// Probs holds one probability per level, lowest level first.
	Probs []float64
	// Levels are the level descriptions from the question, in order.
	Levels []Content
	// Legend is the level descriptions the API echoed back, keyed by level
	// index. It is nil when the answer carried none.
	Legend map[int]Content
	// Confidence is the API's confidence; see [ChoiceAnswer.Confidence].
	Confidence float64
}

// NearestIndex returns the index of the closest whole level.
func (a ScoreAnswer) NearestIndex() int {
	if len(a.Levels) == 0 {
		return 0
	}
	return min(max(int(math.Round(a.Value)), 0), len(a.Levels)-1)
}

// Nearest returns the index and description of the closest whole level. Use
// [Text] to render the description when the levels are structured.
func (a ScoreAnswer) Nearest() (int, Content) {
	if len(a.Levels) == 0 {
		return 0, nil
	}
	i := a.NearestIndex()
	return i, a.Levels[i]
}

func isProb(p float64) bool { return !math.IsNaN(p) && p >= 0 && p <= 1 }

func deref(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}
