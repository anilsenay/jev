package jev_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/anilsenay/jev"
	"github.com/anilsenay/jev/jevtest"
)

type Intent string

const (
	Refund   Intent = "refund"
	Shipping Intent = "shipping"
	Other    Intent = "other"
)

var (
	spamQ = jev.Noul("Is this review spam?",
		jev.NoulCriteria{True: "advertising or gibberish", False: "a genuine review"})
	intentQ = jev.Choice("What does the customer want?",
		jev.Opt(Refund, "wants money back"),
		jev.Opt(Shipping, ""),
		jev.Opt(Other, "anything else"),
	)
	severityQ = jev.Score("How severe is the complaint?", "None", "Mild", "Serious", "Severe")
)

func newClient(t *testing.T, p jev.Provider, opts ...jev.Option) *jev.Client {
	t.Helper()
	c, err := jev.New(append([]jev.Option{jev.WithProvider(p)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func p(v float64) *float64 { return &v }

func TestWireFormat(t *testing.T) {
	var got []byte
	c := newClient(t, jev.ProviderFunc(func(_ context.Context, r *jev.Request) (*jev.Response, error) {
		got, _ = json.Marshal(r)
		return nil, errors.New("stop")
	}), jev.WithModel("jev-1.13"))

	b := c.Batch(map[string]string{"text": "hi"})
	jev.Add(b, spamQ)
	jev.Add(b, intentQ)
	jev.Add(b, severityQ)
	_, _ = b.Run(context.Background())

	want := `{"model":"jev-1.13","state":{"text":"hi"},"questions":{` +
		`"q0":{"type":"noul","instructions":"Is this review spam?","criteria":{"true":"advertising or gibberish","false":"a genuine review"}},` +
		`"q1":{"type":"choice","instructions":"What does the customer want?","criteria":{"other":"anything else","refund":"wants money back","shipping":null}},` +
		`"q2":{"type":"score","instructions":"How severe is the complaint?","criteria":["None","Mild","Serious","Severe"]}}}`
	if string(got) != want {
		t.Fatalf("wire format\n got: %s\nwant: %s", got, want)
	}
}

func TestInvalidQuestions(t *testing.T) {
	// Only what the official SDKs reject before sending, plus the two label
	// checks that have no JavaScript analogue because object keys cannot repeat.
	cases := map[string]func(*jev.Batch){
		"two criteria": func(b *jev.Batch) {
			jev.Add(b, jev.Noul("x?", jev.NoulCriteria{True: "a"}, jev.NoulCriteria{True: "b"}))
		},
		"duplicate option": func(b *jev.Batch) { jev.Add(b, jev.OneOf("x?", "a", "a")) },
		"empty option":     func(b *jev.Batch) { jev.Add(b, jev.OneOf("x?", "a", "")) },
		"one level":        func(b *jev.Batch) { jev.Add(b, jev.Score("x?", "low")) },
		"no questions":     func(b *jev.Batch) {},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			fake := jevtest.New().On(jevtest.Any(), jevtest.Yes(1))
			b := newClient(t, fake).Batch("s")
			build(b)
			if _, err := b.Run(context.Background()); !errors.Is(err, jev.ErrInvalidQuestion) {
				t.Fatalf("err = %v, want ErrInvalidQuestion", err)
			}
			if len(fake.Calls()) != 0 {
				t.Fatal("invalid batch reached the provider")
			}
		})
	}
}

func TestTypedAnswers(t *testing.T) {
	fake := jevtest.New().
		On(jevtest.Instructions("spam"), jevtest.Yes(0.03)).
		On(jevtest.Instructions("customer want"), jevtest.Pick("refund", 0.9)).
		On(jevtest.Instructions("severe"), jevtest.Level(2, 0.7))
	c := newClient(t, fake)

	b := c.Batch("I want my money back, the phone arrived broken.")
	spam := jev.Add(b, spamQ)
	intent := jev.Add(b, intentQ)
	sev := jev.Add(b, severityQ)
	meta, err := b.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if meta.Model != "jevtest" || len(fake.Calls()) != 1 {
		t.Fatalf("meta %+v, calls %d", meta, len(fake.Calls()))
	}

	s, err := spam.Get()
	if err != nil {
		t.Fatal(err)
	}
	if yes, ok := s.Sure(0.95); !ok || yes {
		t.Fatalf("spam Sure = %v,%v want false,true (P=%v)", yes, ok, s.P)
	}
	if _, ok := s.Sure(0.99); ok {
		t.Fatal("spam should not be sure at 0.99")
	}

	i, err := intent.Get()
	if err != nil {
		t.Fatal(err)
	}
	var value Intent = i.Value // compile-time proof the answer is typed
	if v, ok := i.Sure(0.85); !ok || v != Refund || value != Refund {
		t.Fatalf("intent = %v", i)
	}
	if r := i.Ranked(); r[0] != Refund || len(r) != 3 {
		t.Fatalf("ranked = %v", r)
	}

	sv, err := sev.Get()
	if err != nil {
		t.Fatal(err)
	}
	if lvl, name := sv.Nearest(); lvl != 2 || jev.Text(name) != "Serious" {
		t.Fatalf("nearest = %d %q (value %v)", lvl, jev.Text(name), sv.Value)
	}
	if sv.NearestIndex() != 2 {
		t.Fatalf("NearestIndex = %d", sv.NearestIndex())
	}
}

func TestMalformedAnswerOnlyFailsItsHandle(t *testing.T) {
	c := newClient(t, jev.ProviderFunc(func(_ context.Context, r *jev.Request) (*jev.Response, error) {
		return &jev.Response{Answers: map[string]jev.RawAnswer{
			"q0": {Type: jev.KindNoul, Noul: p(0.8)},
			"q1": {Type: jev.KindChoice, Choice: "cancel", Probabilities: map[string]float64{"cancel": 1}},
			"q2": {Type: jev.KindNoul, Noul: p(0.5)}, // wrong kind for a score
			// q3 missing
		}}, nil
	}))
	b := c.Batch("s")
	ok := jev.Add(b, spamQ)
	badChoice := jev.Add(b, intentQ)
	wrongKind := jev.Add(b, severityQ)
	missing := jev.Add(b, jev.Noul("Missing?"))

	if _, err := b.Run(context.Background()); !errors.Is(err, jev.ErrMalformedAnswer) {
		t.Fatalf("Run err = %v", err)
	}
	if a, err := ok.Get(); err != nil || a.P != 0.8 {
		t.Fatalf("good handle: %v %v", a, err)
	}
	for name, err := range map[string]error{
		"bad choice": second(badChoice.Get()),
		"wrong kind": second(wrongKind.Get()),
		"missing":    second(missing.Get()),
	} {
		if !errors.Is(err, jev.ErrMalformedAnswer) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func second[A any](_ A, err error) error { return err }

func TestHandleLifecycle(t *testing.T) {
	c := newClient(t, jevtest.New().On(jevtest.Any(), jevtest.Yes(0.9)))
	b := c.Batch("s")
	h := jev.Add(b, spamQ)
	if _, err := h.Get(); !errors.Is(err, jev.ErrNotRun) {
		t.Fatalf("before run: %v", err)
	}
	if _, err := b.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Run(context.Background()); !errors.Is(err, jev.ErrBatchUsed) {
		t.Fatalf("second run: %v", err)
	}
	late := jev.Add(b, spamQ)
	if _, err := late.Get(); !errors.Is(err, jev.ErrBatchUsed) {
		t.Fatalf("late add: %v", err)
	}
}

func TestProviderErrorReachesEveryHandle(t *testing.T) {
	boom := errors.New("boom")
	c := newClient(t, jevtest.New().On(jevtest.Any(), jevtest.Fail(boom)))
	b := c.Batch("s")
	h1, h2 := jev.Add(b, spamQ), jev.Add(b, intentQ)
	if _, err := b.Run(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("run: %v", err)
	}
	if !errors.Is(second(h1.Get()), boom) || !errors.Is(second(h2.Get()), boom) {
		t.Fatal("handles should report provider error")
	}
}

func TestFakeFailsClosedWithoutRule(t *testing.T) {
	c := newClient(t, jevtest.New().On(jevtest.Instructions("spam"), jevtest.Yes(0.1)))
	_, err := jev.Ask(context.Background(), c, "s", intentQ)
	if err == nil || !strings.Contains(err.Error(), "no rule") {
		t.Fatalf("err = %v", err)
	}
}

func TestAskAndCache(t *testing.T) {
	fake := jevtest.New().On(jevtest.Any(), jevtest.Pick("shipping", 0.8))
	c := newClient(t, fake, jev.WithCache(jev.NewMemoryCache(10)))
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		a, err := jev.Ask(ctx, c, map[string]any{"b": 2, "a": 1}, intentQ)
		if err != nil || a.Value != Shipping {
			t.Fatalf("ask: %v %v", a, err)
		}
	}
	if _, err := jev.Ask(ctx, c, "different", intentQ); err != nil {
		t.Fatal(err)
	}
	if n := len(fake.Calls()); n != 2 {
		t.Fatalf("provider calls = %d, want 2", n)
	}
}

func TestMemoryCacheEvicts(t *testing.T) {
	c := jev.NewMemoryCache(2)
	c.Set("a", &jev.Response{Model: "a"})
	c.Set("b", &jev.Response{Model: "b"})
	c.Get("a") // a is now most recent
	c.Set("c", &jev.Response{Model: "c"})
	if _, ok := c.Get("b"); ok {
		t.Fatal("b should be evicted")
	}
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a should survive")
	}
}

func TestNoAPIKey(t *testing.T) {
	t.Setenv(jev.APIKeyEnv, "")
	if _, err := jev.New(); !errors.Is(err, jev.ErrNoAPIKey) {
		t.Fatalf("err = %v", err)
	}
}
