// Package jev asks Jev, the decision model behind TypeSafe's System One API,
// questions whose answers arrive as ordinary Go types.
//
// An LLM hands back prose and leaves the parsing to the caller. Jev never
// leaves the set of answers the caller defined, and reports how probable each
// one was. This package carries that guarantee into the type system rather than
// stopping at the JSON boundary: a [Choice] over a string enum yields that
// enum, so the compiler checks the switch written on it.
//
// # Asking one question
//
//	type Intent string
//
//	const (
//		Refund   Intent = "refund"
//		Shipping Intent = "shipping"
//		Other    Intent = "other"
//	)
//
//	var intentQ = jev.Choice("What does the customer want?",
//		jev.Opt(Refund, "wants money back"),
//		jev.Opt(Shipping, "asks where the order is"),
//		jev.Opt(Other, "anything else"),
//	)
//
//	ans, err := jev.Ask(ctx, client, ticket, intentQ)
//	if err != nil {
//		return err
//	}
//	if intent, sure := ans.Sure(0.9); sure && intent == Refund {
//		return autoRefund(ticket)
//	}
//
// # Asking many questions in one call
//
// Independent questions about the same state belong in one [Batch]: they are
// answered together, for the price of a single round trip. Each [Add] hands
// back a typed [Handle]:
//
//	b := client.Batch(review)
//	spam := jev.Add(b, jev.Noul("Is this review spam or advertising?"))
//	intent := jev.Add(b, intentQ)
//	if _, err := b.Run(ctx); err != nil {
//		return err
//	}
//	s, err := spam.Get()     // jev.NoulAnswer
//	i, err := intent.Get()   // jev.ChoiceAnswer[Intent]
//
// # Questions can carry structure
//
// Instructions, option descriptions, score levels and noul criteria are all
// [Content]: plain text, or anything that encodes as a JSON object or array.
// Reach for structure when a sentence keeps failing to separate two options, or
// when the material is already JSON and flattening it would only lose detail:
//
//	jev.Choice("Which department does this product belong to?",
//		jev.Opt(Sporting, map[string]any{
//			"Cycling": []string{"Bike Bottles & Cages", "Helmets"},
//			"Fitness": []string{"Yoga Mats"},
//		}),
//		jev.Opt(Drinkware, map[string]any{
//			"what":    "Everyday water bottles, travel mugs, tumblers",
//			"not_for": "Sport-specific gear",
//		}),
//	)
//
// # When the API moves first
//
// [RawQuestion] sends a [Spec] exactly as given and returns the answer
// undecoded, so a question kind or field that lands before a release of this
// package does is still reachable. [Spec.Extra] carries unmodelled fields.
//
// # Policy stays in your code
//
// Low confidence is not an error. Answers carry the full distribution, and the
// thresholds that decide what to do with it belong to the caller. Errors are
// reserved for invalid questions, transport and API failures, and answers that
// do not fit the question.
//
// The compiler checks that an answer fits its question, never that it is right.
// Measure that on labelled inputs of your own and set the thresholds from what
// you find; a threshold is one number in your code, changed without touching a
// question or paying for another request.
//
// # Configuration
//
// [New] reads TYPESAFE_API_KEY, TYPESAFE_BASE_URL and TYPESAFE_DEFAULT_MODEL
// when the matching option is not given. Callers whose configuration comes from
// somewhere else pass the key directly:
//
//	client, err := jev.NewWithKey(cfg.TypeSafeKey)
//
// An explicitly supplied key disables the environment fallback entirely, so an
// empty one is [ErrNoAPIKey] rather than a silent fall back.
//
// # Testing
//
// Package [github.com/anilsenay/jev/jevtest] provides a rule-based fake
// [Provider], so code that branches on Jev can be tested without the network.
package jev
