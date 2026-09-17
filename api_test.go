package jev_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/anilsenay/jev"
	"github.com/anilsenay/jev/jevtest"
)

type Dept string

const (
	Sporting Dept = "Sporting Goods"
	Kitchen  Dept = "Home and Kitchen"
)

// Structured content is what the docs call an EntryType: instructions, option
// descriptions, score levels and noul criteria all accept JSON.
func TestStructuredContentWireFormat(t *testing.T) {
	var got []byte
	c := newClient(t, jev.ProviderFunc(func(_ context.Context, r *jev.Request) (*jev.Response, error) {
		got, _ = json.Marshal(r)
		return nil, errors.New("stop")
	}), jev.WithModel("jev-latest"))

	b := c.Batch("32oz bottle with a flip straw lid.")
	jev.Add(b, jev.Choice(
		map[string]any{"question": "Which department?", "focus": "the primary use"},
		jev.Opt(Sporting, map[string]any{"Cycling": []string{"Bike Bottles and Cages"}}),
		jev.Opt(Kitchen, nil),
	))
	jev.Add(b, jev.Noul(
		map[string]any{"question": "Is it drinkware?"},
		jev.NoulCriteria{True: map[string]any{"what": "holds a drink"}},
	))
	jev.Add(b, jev.Score("How focused?",
		map[string]any{"summary": "one change"},
		map[string]any{"summary": "several changes"},
	))
	_, _ = b.Run(context.Background())

	want := `{"model":"jev-latest","state":"32oz bottle with a flip straw lid.","questions":{` +
		`"q0":{"type":"choice","instructions":{"focus":"the primary use","question":"Which department?"},` +
		`"criteria":{"Home and Kitchen":null,"Sporting Goods":{"Cycling":["Bike Bottles and Cages"]}}},` +
		`"q1":{"type":"noul","instructions":{"question":"Is it drinkware?"},"criteria":{"true":{"what":"holds a drink"}}},` +
		`"q2":{"type":"score","instructions":"How focused?","criteria":[{"summary":"one change"},{"summary":"several changes"}]}}}`
	if string(got) != want {
		t.Fatalf("wire format\n got: %s\nwant: %s", got, want)
	}
}

// The API echoes a score's levels back in "legend", and those levels may be
// objects. Decoding must survive that.
func TestStructuredLegendDecodes(t *testing.T) {
	body := `{"model":"jev-1.13.0","answers":{"q0":{"type":"score","score":1.0,"confidence":0.99,` +
		`"legend":{"0":{"summary":"fine"},"1":{"summary":"bad"}},"probabilities":{"0":0.0,"1":1.0}}},` +
		`"usage":{"input_tokens":1,"output_tokens":1}}`
	s := server(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) })

	q := jev.Score("How bad?", map[string]any{"summary": "fine"}, map[string]any{"summary": "bad"})
	ans, err := jev.Ask(context.Background(), httpClient(t, s.URL), "x", q)
	if err != nil {
		t.Fatal(err)
	}
	i, level := ans.Nearest()
	if i != 1 || jev.Text(level) != `{"summary":"bad"}` {
		t.Fatalf("nearest = %d %q", i, jev.Text(level))
	}
	if jev.Text(ans.Legend[0]) != `{"summary":"fine"}` || len(ans.Legend) != 2 {
		t.Fatalf("legend = %v", ans.Legend)
	}
	if ans.Confidence != 0.99 {
		t.Fatalf("confidence = %v", ans.Confidence)
	}
}

func TestMalformedDistributions(t *testing.T) {
	scoreQ := jev.Score("How bad?", "fine", "bad", "awful")
	cases := map[string]struct {
		q   jev.Question[jev.ScoreAnswer]
		raw jev.RawAnswer
	}{
		"legend key is not an index": {scoreQ, jev.RawAnswer{
			Type:          jev.KindScore,
			Score:         p(1),
			Legend:        map[string]json.RawMessage{"low": json.RawMessage(`"fine"`)},
			Probabilities: map[string]float64{"1": 1},
		}},
		"probability for unknown level": {scoreQ, jev.RawAnswer{
			Type:          jev.KindScore,
			Score:         p(1),
			Probabilities: map[string]float64{"7": 1},
		}},
		"score out of range": {scoreQ, jev.RawAnswer{
			Type:          jev.KindScore,
			Score:         p(9),
			Probabilities: map[string]float64{"1": 1},
		}},
		"no probabilities": {scoreQ, jev.RawAnswer{Type: jev.KindScore, Score: p(1)}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := newClient(t, jev.ProviderFunc(func(_ context.Context, _ *jev.Request) (*jev.Response, error) {
				return &jev.Response{Answers: map[string]jev.RawAnswer{"q0": tc.raw}}, nil
			}))
			if _, err := jev.Ask(context.Background(), c, "x", tc.q); !errors.Is(err, jev.ErrMalformedAnswer) {
				t.Fatalf("err = %v, want ErrMalformedAnswer", err)
			}
		})
	}

	t.Run("choice without probabilities", func(t *testing.T) {
		c := newClient(t, jev.ProviderFunc(func(_ context.Context, _ *jev.Request) (*jev.Response, error) {
			return &jev.Response{Answers: map[string]jev.RawAnswer{
				"q0": {Type: jev.KindChoice, Choice: "refund"},
			}}, nil
		}))
		if _, err := jev.Ask(context.Background(), c, "x", intentQ); !errors.Is(err, jev.ErrMalformedAnswer) {
			t.Fatalf("err = %v, want ErrMalformedAnswer", err)
		}
	})
}

// The API accepts a choice with a single option; only an empty set is rejected.
func TestSingleOptionChoiceIsValid(t *testing.T) {
	fake := jevtest.New().On(jevtest.Any(), jevtest.Pick("only", 1))
	ans, err := jev.Ask(context.Background(), newClient(t, fake), "x", jev.OneOf("Which?", "only"))
	if err != nil {
		t.Fatal(err)
	}
	if v, sure := ans.Sure(0.99); !sure || v != "only" {
		t.Fatalf("answer = %v %v", v, sure)
	}
}

func TestRequestIDReachesMetaAndError(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		s := server(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Typesafe-Request-Id", "req_abc")
			_, _ = io.WriteString(w, okBody)
		})
		b := httpClient(t, s.URL).Batch("x")
		jev.Add(b, spamQ)
		meta, err := b.Run(context.Background())
		if err != nil || meta.RequestID != "req_abc" {
			t.Fatalf("meta %+v err %v", meta, err)
		}
	})

	t.Run("error", func(t *testing.T) {
		s := server(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Typesafe-Request-Id", "req_def")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"detail":{"error_type":"authentication_error","message":"bad key"}}`)
		})
		_, err := jev.Ask(context.Background(), httpClient(t, s.URL), "x", spamQ)
		var apiErr *jev.APIError
		if !errors.As(err, &apiErr) || apiErr.RequestID != "req_def" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("legacy header", func(t *testing.T) {
		s := server(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Request-Id", "req_old")
			_, _ = io.WriteString(w, okBody)
		})
		b := httpClient(t, s.URL).Batch("x")
		jev.Add(b, spamQ)
		meta, _ := b.Run(context.Background())
		if meta.RequestID != "req_old" {
			t.Fatalf("meta %+v", meta)
		}
	})
}

func TestAPIErrorBodyIsParsed(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		check  func(*testing.T, *jev.APIError)
	}{
		"typed object": {
			http.StatusUnauthorized,
			`{"detail":{"error_type":"authentication_error","message":"Cannot authenticate with the server."}}`,
			func(t *testing.T, e *jev.APIError) {
				if e.Type != "authentication_error" || e.Message != "Cannot authenticate with the server." {
					t.Fatalf("%+v", e)
				}
				if !errors.Is(e, jev.ErrAuth) {
					t.Fatal("should be ErrAuth")
				}
			},
		},
		"plain string": {
			http.StatusBadRequest,
			`{"detail":"Too many score levels. Must have at most 10 levels."}`,
			func(t *testing.T, e *jev.APIError) {
				if e.Message != "Too many score levels. Must have at most 10 levels." || e.Type != "" {
					t.Fatalf("%+v", e)
				}
				if !errors.Is(e, jev.ErrInvalidRequest) {
					t.Fatal("should be ErrInvalidRequest")
				}
			},
		},
		"validation entries": {
			http.StatusUnprocessableEntity,
			`{"detail":[{"type":"missing","loc":["body","model"],"msg":"Field required","input":{}}]}`,
			func(t *testing.T, e *jev.APIError) {
				if len(e.Fields) != 1 {
					t.Fatalf("%+v", e)
				}
				f := e.Fields[0]
				if f.Type != "missing" || f.Msg != "Field required" || f.String() != "model: Field required" {
					t.Fatalf("%+v", f)
				}
			},
		},
		"unparsable": {
			http.StatusBadRequest,
			`not json at all`,
			func(t *testing.T, e *jev.APIError) {
				if e.Message != "" || e.Body != "not json at all" {
					t.Fatalf("%+v", e)
				}
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := server(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			_, err := jev.Ask(context.Background(), httpClient(t, s.URL, jev.WithRetries(0)), "x", spamQ)
			var apiErr *jev.APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("err = %v", err)
			}
			tc.check(t, apiErr)
		})
	}
}

func TestModels(t *testing.T) {
	s := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Method != http.MethodGet {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"models":[
			{"name":"jev-latest","description":"The latest Jev","release_date":"2026-09-10T18:38:01.391457Z"},
			{"name":"jev-preview","description":"A preview","release_date":"2026-09-10T18:39:06.057655Z"}]}`)
	})
	models, err := httpClient(t, s.URL).Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].Name != "jev-latest" || models[1].Name != "jev-preview" {
		t.Fatalf("models = %+v", models)
	}
	if models[0].ReleaseDate.Year() != 2026 {
		t.Fatalf("release date = %v", models[0].ReleaseDate)
	}
}

func TestModelsNeedsAModelLister(t *testing.T) {
	c := newClient(t, jevtest.New())
	if _, err := c.Models(context.Background()); !errors.Is(err, jev.ErrNoModelList) {
		t.Fatalf("err = %v, want ErrNoModelList", err)
	}
}
