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
// The interface is sealed. Implementing it outside this package would let a
// question be built that the API has no way to answer, so the compiler stops
// that; [RawQuestion] is the deliberate way around it.
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

// ---------------------------------------------------------------- raw

type rawQ struct{ raw Spec }

// RawQuestion sends a hand-built [Spec] exactly as given and hands the answer
// back undecoded. Nothing about it is validated or narrowed.
//
// It exists so the API can move ahead of this package: a question kind or field
// that lands before a release does is still reachable, through [Spec.Extra] for
// new fields and a new [Kind] for a new type.
//
//	q := jev.RawQuestion(jev.Spec{
//		Type:         jev.KindChoice,
//		Instructions: "Which team should handle this?",
//		Criteria:     map[string]jev.Content{"billing": nil, "shipping": nil},
//		Extra:        map[string]any{"beam_width": 4},
//	})
//	h := jev.Add(b, q)     // *Handle[RawAnswer]
//	raw, err := h.Get()    // read raw.Choice, raw.Probabilities yourself
//
// Reach for a typed constructor whenever one fits; this gives up the type
// safety that is the point of the package.
func RawQuestion(spec Spec) Question[RawAnswer] { return rawQ{spec} }

func (q rawQ) spec() (Spec, error)                     { return q.raw, nil }
func (q rawQ) decode(raw RawAnswer) (RawAnswer, error) { return raw, nil }

// ---------------------------------------------------------------- noul

// NoulCriteria spells out the two outcomes. Either side may be left nil, and
// either may be structured [Content] when one sentence cannot hold them
// apart.
type NoulCriteria struct {
	// True describes the outcome the probability climbs towards.
	True Content
	// False describes the outcome it falls towards.
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

// Noul settles a question that has two outcomes, answering with how likely the
// affirmative one is. Pass a [NoulCriteria] to spell the outcomes out; doing so
// is usually what moves a hedged number towards a decisive one. The API wants
// instructions, criteria, or both.
//
// When several labels can be true at the same time, ask a separate Noul for
// each rather than forcing a [Choice] between them. And read 0.5 as "the model
// cannot separate yes from no", not as "somewhere in the middle" — a middle is
// what a [Score] is for.
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

// NoulAnswer is how likely the model finds a yes, from 0 to 1. A noul reports
// no separate confidence, because this number already is one.
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

// Opt builds a [ChoiceOption]. T is inferred from value, so handing it a
// constant of your own string type is what makes the whole choice typed. A nil
// description leaves the option to stand on its name.
func Opt[T ~string](value T, description Content) ChoiceOption[T] {
	return ChoiceOption[T]{Value: value, Description: description}
}

type choiceQ[T ~string] struct {
	instructions Content
	options      []ChoiceOption[T]
}

// Choice settles on exactly one of the options and reports how the probability
// was spread over all of them. The set is a closed world — there is no answer
// outside it — so offer somewhere for an input to land that none of the real
// options describe.
func Choice[T ~string](instructions Content, options ...ChoiceOption[T]) Question[ChoiceAnswer[T]] {
	return choiceQ[T]{instructions, append([]ChoiceOption[T](nil), options...)}
}

// OneOf is [Choice] with no descriptions at all, for a set of options that
// need no explaining.
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
		// An option left undescribed is null on the wire; "" would read as a
		// description that happens to be empty.
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

// Score places the state on a scale whose points you describe, lowest first.
// Write each level as a situation someone could recognise, not as a grade: the
// answer is a probability-weighted position along them, so it may come to rest
// short of any single level — a reading in its own right, not a rounding
// error.
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

// ScoreAnswer is where a [Score] came to rest on its own scale.
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

// Nearest rounds to the level the answer sits closest to and returns it with
// its description. Pass the description through [Text] to print it when the
// levels are structured.
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
