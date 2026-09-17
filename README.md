# jev

Type-safe Go client for [TypeSafe](https://typesafe.ai)'s System One API and its model, **Jev**.

Jev answers questions whose possible answers you define up front, with a probability for each.
This package makes those answers **Go types**: a choice over your own enum returns that enum,
and invalid questions or answers that don't fit are caught before your code branches on them.

```bash
go get github.com/anilsenay/jev
```

> Unofficial. Requires an API key from TypeSafe's early access (`TYPESAFE_API_KEY`).

## Quick start

```go
type Intent string

const (
	Refund   Intent = "refund"
	Shipping Intent = "shipping"
	Other    Intent = "other"
)

// Declare questions once.
var intentQ = jev.Choice("What does the customer want?",
	jev.Opt(Refund, "wants money back"),
	jev.Opt(Shipping, "asks where the order is"),
	jev.Opt(Other, "anything else"),
)

client, err := jev.New() // reads TYPESAFE_API_KEY

ans, err := jev.Ask(ctx, client, ticket, intentQ) // ans is jev.ChoiceAnswer[Intent]
if err != nil {
	return err
}

switch intent, sure := ans.Sure(0.9); {
case sure && intent == Refund:
	return autoRefund(ticket)
case !sure:
	return escalate(ticket, ans.Probs)
default:
	return route(intent)
}
```

## Question kinds

| Constructor | Answer | Use for |
| --- | --- | --- |
| `jev.Noul(q)` / `jev.Noul(q, jev.NoulCriteria{True, False})` | `NoulAnswer{P}` | whether a condition holds; one per label for multi-label |
| `jev.Choice(q, jev.Opt(v, desc)...)` / `jev.OneOf(q, v...)` | `ChoiceAnswer[T]{Value, Probs, Confidence}` | exactly one of a set you define |
| `jev.Score(q, levels...)` | `ScoreAnswer{Value, Probs, Levels, Legend, Confidence}` | a degree along ordered levels; can land between them |

Instructions are optional, as in the official SDKs: a choice or score whose criteria say everything
can pass `nil`. Like those SDKs, this package validates only what it can be sure of before sending —
at least one question, at least two score levels, and no duplicate or empty choice labels. The API's
own limits (`MaxChoiceOptions`, `MaxScoreLevels`) are exported for reference but not enforced, so a
raised server limit never blocks a valid request.

`Sure(threshold)` is available on noul and choice answers. Low confidence is **not** an error:
thresholds are policy and live in your code.

`Confidence` is the API's own field: how peaked the distribution is. It is **not** a calibrated
probability that the answer is right — use `Probs[Value]` (or `P`) for that. A noul carries no
separate confidence; its probability is the signal.

### Noul — is this true?

A yes/no judgment where the probability itself is the signal. Describe both ends when the boundary
is subtle; one noul per label when several labels can apply at once.

```go
spam := jev.Noul("Is this review spam or advertising?", jev.NoulCriteria{
	True:  "promotes an unrelated product, or links to another site",
	False: "a genuine opinion about the product",
})

ans, err := jev.Ask(ctx, client, review, spam) // jev.NoulAnswer
if err != nil {
	return err
}

switch yes, sure := ans.Sure(0.9); {
case sure && yes:
	return hide(review)      // ans.P == 0.99 for the advertising example below
case sure:
	return publish(review)
default:
	return queueForModeration(review, ans.P)
}
```

```text
state: "BEST DEAL EVER!!! visit cheap-watches-online.example for 90% off luxury watches"
→ P = 0.99   Sure(0.9) = true, true
```

`Sure(t)` answers in both directions: `(true, true)` when `P >= t`, `(false, true)` when `1-P >= t`,
and `(false, false)` in the uncertain middle. Use a threshold above 0.5.

### Choice — which one of these?

One option out of a set you define, returned as **your own enum**. The model cannot answer outside
the set, so include a catch-all when the list might not cover every input.

```go
type Team string

const (
	Billing   Team = "billing"
	Shipping  Team = "shipping"
	Technical Team = "technical"
	Other     Team = "other"
)

route := jev.Choice("Which team should handle this ticket?",
	jev.Opt(Billing, "Charges, invoices, refunds, subscriptions"),
	jev.Opt(Shipping, "Delivery status, delays, lost packages"),
	jev.Opt(Technical, "Bugs, outages, integrations"),
	jev.Opt(Other, nil), // nil when the name speaks for itself
)

ans, err := jev.Ask(ctx, client, ticket, route) // jev.ChoiceAnswer[Team]
if err != nil {
	return err
}

team, sure := ans.Sure(0.8) // team is a Team, checked at compile time
if !sure {
	return escalate(ticket, ans.Ranked()) // options, most probable first
}
return assign(ticket, team)
```

```text
state: "I was charged twice for order A-104 last Friday. Please refund the duplicate."
→ Value = billing   Confidence = 1.00
  Probs  = map[billing:1 other:0 shipping:0 technical:0]
  Ranked = [billing other shipping technical]
```

`jev.OneOf("Which team?", Billing, Shipping, Technical, Other)` is the same question without
descriptions. `Probs` always holds every option, including the zero ones.

### Score — where on this scale?

A position along ordered levels, lowest first. The answer is probability-weighted, so it can land
*between* two levels — which is information, not noise.

```go
severity := jev.Score("How severe is the reported issue?",
	"Cosmetic; no impact to functionality",
	"Broken or degraded feature, but a workaround exists",
	"Blocking issue; no workaround exists",
)

ans, err := jev.Ask(ctx, client, report, severity) // jev.ScoreAnswer
if err != nil {
	return err
}

level, description := ans.Nearest()
log.Printf("severity %.2f — nearest level %d: %s", ans.Value, level, jev.Text(description))

if ans.Value >= 1.5 || ans.Probs[2] > 0.4 { // thresholds are yours
	return page(onCall)
}
```

```text
state: "The export button crashes the settings page in Safari. Works in Chrome,
        but some customers only use Safari."
→ Value = 1.47   Confidence = 0.30
  Probs = [0 0.53 0.47]
  Nearest = 1, "Broken or degraded feature, but a workaround exists"
```

That answer sits almost exactly between "has a workaround" and "blocking", and the low `Confidence`
says so. Rounding it to a single level would throw that away; thresholding `Value` or reading
`Probs` directly keeps it. Scores need at least two levels, and the API accepts at most ten.

## Questions can carry structure

`instructions`, choice option descriptions, score levels and noul criteria are all `jev.Content`:
a string, or any value that marshals to a JSON object or array. Use structure when a question has
several parts, or when the supporting data is already JSON.

```go
jev.Choice("Which department does this product belong to?",
	jev.Opt(Sporting, map[string]any{
		"Cycling": []string{"Bike Bottles & Cages", "Helmets"},
		"Fitness": []string{"Yoga Mats"},
	}),
	jev.Opt(Drinkware, map[string]any{
		"what":    "Everyday water bottles, travel mugs, tumblers",
		"not_for": "Sport-specific gear",
	}),
	jev.Opt(Other, nil), // nil description is sent as JSON null
)
```

`jev.Text(c)` renders a `Content` for logging: a string as it is, anything else as compact JSON.

## When the API moves first

New API fields and question kinds land before a release of this package does. `RawQuestion` sends a
`Spec` exactly as given and hands the answer back undecoded, so nothing blocks you in the meantime —
the counterpart of the official SDKs' raw question dictionaries and `extra_body`.

```go
q := jev.RawQuestion(jev.Spec{
	Type:         "rank",                          // a kind this package predates
	Instructions: "Rank these passages by relevance",
	Criteria:     []jev.Content{"passage A", "passage B"},
	Extra:        map[string]any{"beam_width": 4},  // a field it predates too
})

h := jev.Add(b, q)  // *jev.Handle[jev.RawAnswer]
if _, err := b.Run(ctx); err != nil {
	return err
}
raw, err := h.Get() // read raw.Choice, raw.Probabilities, raw.Score yourself
```

Nothing is validated or narrowed here, so reach for a typed constructor whenever one fits. `Extra`
works on any `Spec`; `type`, `instructions` and `criteria` are reserved and cannot be overridden
from it.

## Many questions, one request

Independent questions about the same state should share a batch. They run in parallel on the
model and cost one call.

```go
b := client.Batch(review)
spam     := jev.Add(b, jev.Noul("Is this review spam or advertising?"))
intent   := jev.Add(b, intentQ)
severity := jev.Add(b, jev.Score("How severe is the complaint?", "None", "Mild", "Serious", "Severe"))

meta, err := b.Run(ctx) // meta.Model, meta.Usage, meta.RequestID, meta.Latency

s, err := spam.Get()     // jev.NoulAnswer
i, err := intent.Get()   // jev.ChoiceAnswer[Intent]
v, err := severity.Get() // jev.ScoreAnswer
level, name := v.Nearest()
```

If one answer doesn't fit its question, `Run` returns an error wrapping `jev.ErrMalformedAnswer`,
but the other handles can still be read.

## Models

```go
models, err := client.Models(ctx) // []jev.ModelCard{Name, Description, ReleaseDate}
```

Needs a provider that implements `ModelLister`, which the default HTTP provider does; a fake that
does not returns `ErrNoModelList`. Middleware does not hide it.

## Errors

One sentinel per status the official SDKs distinguish, matched with `errors.Is`:

| Status | Sentinel | Retried |
| --- | --- | --- |
| 400 | `ErrBadRequest`, `ErrInvalidRequest` | no |
| 401 | `ErrAuth` | no |
| 403 | `ErrPermissionDenied` | no |
| 404 | `ErrNotFound` | no |
| 408 | – | yes |
| 422 | `ErrUnprocessable`, `ErrInvalidRequest` | no |
| 429 | `ErrRateLimit` | yes |
| 529 | `ErrOverloaded`, `ErrInternalServer` | yes |
| other 5xx | `ErrInternalServer` | yes |

Below HTTP: `ErrConnection` for a request that never got a response, and `ErrTimeout` (which also
matches `ErrConnection`) for one that ran out of time.

Before any request: `ErrInvalidQuestion`, `ErrNoAPIKey`, `ErrNotRun`, `ErrBatchUsed`,
`ErrNoModelList`. After one: `ErrMalformedAnswer` for an answer that does not fit its question.

`APIError` parses the API's body, so `Message` carries the human-readable reason and `Fields`
lists the offending fields of a 422:

```go
var apiErr *jev.APIError
if errors.As(err, &apiErr) {
	log.Printf("%s (%s) request=%s", apiErr.Message, apiErr.Type, apiErr.RequestID)
}
```

## Configuration

```go
client, err := jev.New(
	jev.WithAPIKey(key),                 // default: $TYPESAFE_API_KEY
	jev.WithBaseURL(url),                // default: $TYPESAFE_BASE_URL, then https://api.typesafe.ai
	jev.WithModel("jev-preview"),        // default: $TYPESAFE_DEFAULT_MODEL, then jev-latest
	jev.WithTimeout(2*time.Second),      // per attempt, via context; default 10s
	jev.WithRetry(jev.DefaultRetry),     // or WithRetries(n) to change only the count
	jev.WithHeaders(map[string]string{"X-Tenant": "acme"}),
	jev.WithLogger(slog.Default()),      // one line per request; off by default
	jev.WithHTTPClient(hc),              // never modified
	jev.WithCache(jev.NewMemoryCache(10_000)),
	jev.WithMiddleware(metrics),
)
```

Explicit options win over environment variables, which win over the defaults — the same precedence
as the Python and JavaScript SDKs.

Reading configuration from somewhere other than the process environment needs no ceremony:

```go
// godotenv, viper, koanf, a secret manager — anything that hands you a string.
client, err := jev.NewWithKey(cfg.TypeSafeKey, jev.WithTimeout(5*time.Second))
```

Once `WithAPIKey` (or `NewWithKey`) is given a key, `TYPESAFE_API_KEY` is not read at all, so an
empty key is `ErrNoAPIKey` rather than a silent fall back to whatever the machine happens to hold.

### Retries

`DefaultRetry` matches the official SDKs: two retries, 500ms of backoff doubling to 5s with 25%
jitter subtracted, honouring `retry-after-ms` and `Retry-After` up to one minute (a longer delay
falls back to backoff), retrying 408, 429 and every 5xx, plus connection failures and timeouts.

```go
jev.WithRetry(jev.Retry{
	MaxRetries:        3,
	BackoffInitial:    time.Second,
	BackoffMax:        20 * time.Second,
	BackoffJitter:     0.25,
	Statuses:          []int{429, 503}, // nil means 408, 429 and 5xx
	RespectRetryAfter: true,
	MaxRetryAfter:     time.Minute,
	RetryConnection:   true,
	RetryTimeout:      true,
})
```

Every request carries `User-Agent`, `X-TypeSafe-SDK` and `X-TypeSafe-Runtime`, and retries carry
`X-TypeSafe-Retry-Count`, as the official clients do.

`Provider` and `Middleware` are small interfaces, so logging, metrics, rate limiting or an
alternative backend can be plugged in without touching call sites.

## Testing

`jevtest` is a rule-based fake. A question that matches no rule fails the request, so a missing
rule becomes a test failure instead of a silent "no".

```go
fake := jevtest.New().
	On(jevtest.Instructions("spam"), jevtest.Yes(0.02)).
	On(jevtest.Kind(jev.KindChoice), jevtest.Pick("refund", 0.93)).
	On(jevtest.State("broken"), jevtest.Level(2, 0.8))

client, _ := jev.New(jev.WithProvider(fake))
// ... exercise your code, then inspect fake.Calls()
```

Matchers: `Any`, `Instructions`, `Kind`, `Option`, `State`, `All`, `Not`.
Answers: `Yes`, `Pick`, `Level`, `Fail`.

## A note on trust

Typed output guarantees the *shape* of an answer, not its *truth*. Calibration is a property of
predictions in aggregate: measure accuracy per confidence band on your own labelled data before
choosing thresholds for production.

## License

MIT
