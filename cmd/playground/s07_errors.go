package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

// Scenario 7: the edges. Model listing, request IDs, typed errors, pinned model
// versions, and context cancellation.
func errorsAndMetadata(ctx context.Context, c *typesafe.Client) error {
	models, err := c.ListModels(ctx)
	if err != nil {
		return err
	}
	for _, m := range models {
		fmt.Printf("  model: %-12s (%s) %s\n", m.Name, m.ReleaseDate[:10], m.Description)
	}

	// Bad key -> 401 with error_type authentication_error. No retries on 4xx.
	bad, err := typesafe.New(typesafe.WithAPIKey("sk-invalid"), typesafe.WithMaxRetries(0))
	if err != nil {
		return err
	}
	_, err = bad.SystemOne(ctx, typesafe.Request{State: "x", Questions: typesafe.Questions{"q": typesafe.Noul("?")}})
	describe("bad key", err)

	// Empty questions -> 422 validation_error with the offending field.
	_, err = c.SystemOne(ctx, typesafe.Request{State: "x", Questions: typesafe.Questions{}})
	describe("empty questions", err)

	// Malformed question -> 422 as well.
	_, err = c.SystemOne(ctx, typesafe.Request{State: "x", Questions: typesafe.Questions{
		"q": {Type: "score", Instructions: "?", Criteria: []any{"only one level"}},
	}})
	describe("one-level score", err)

	// Unknown model -> whatever the server says; the error carries the request id.
	_, err = c.SystemOne(ctx, typesafe.Request{State: "x", Model: "sos-0.0.0", Questions: typesafe.Questions{"q": typesafe.Noul("?")}})
	describe("unknown model", err)

	// Pinned version vs alias: the response reports the resolved id either way.
	pinned, err := c.SystemOne(ctx, typesafe.Request{State: "x", Model: "sos-1.13.0", Questions: typesafe.Questions{"q": typesafe.Noul("Is `x` a letter?")}})
	if err != nil {
		return err
	}
	fmt.Printf("  pinned jev-1.13.0 responded as: %s (request %s)\n", pinned.Model, pinned.RequestID)

	// Cancelled context -> ConnectionError wrapping context.Canceled, no retry.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = c.SystemOne(cctx, typesafe.Request{State: "x", Questions: typesafe.Questions{"q": typesafe.Noul("?")}})
	describe("cancelled context", err)
	return nil
}

func describe(label string, err error) {
	var ae *typesafe.APIError
	var ce *typesafe.ConnectionError
	switch {
	case err == nil:
		fmt.Printf("  %s -> accepted by the server (no error)\n", label)
	case errors.As(err, &ae):
		fmt.Printf("  %s -> APIError status=%d type=%s retryable=%v request=%s\n      %s\n",
			label, ae.Status, ae.Type, ae.Retryable(), ae.RequestID, truncate(ae.Message, 110))
	case errors.As(err, &ce):
		fmt.Printf("  %s -> ConnectionError canceled=%v: %s\n", label, errors.Is(err, context.Canceled), truncate(ce.Err.Error(), 80))
	default:
		fmt.Printf("  %s -> %T: %v\n", label, err, err)
	}
}
