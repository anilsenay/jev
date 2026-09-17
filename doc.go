// Package jev is a type-safe Go client for TypeSafe's System One API and its
// model Jev.
//
// Jev reads natural language and structured state like an LLM, but instead of
// generating text it answers questions whose possible answers you define in
// advance, with a probability for each. This package turns those questions into
// Go types, so the answer to a Choice over your own enum comes back as that enum.
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
// Independent questions about the same state belong in one [Batch]. They are
// evaluated in parallel and cost one request. Each [Add] returns a typed
// [Handle]:
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
// [Content]: a string, or any value that marshals to JSON. Structure sharpens a
// boundary the model keeps getting wrong, and lets a taxonomy or a schema be
// passed through instead of flattened into a sentence:
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
// Typed output guarantees the shape of an answer, not its truth. Validate your
// thresholds on labelled data before trusting them in production.
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
