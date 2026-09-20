package sos

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

// EvaluationRequest is a constrained provider question made by the toolchain.
// Budget is mandatory: compiler/editor callers cannot bypass request admission.
type EvaluationRequest struct {
	State       any
	Question    typesafe.Question
	Model       string
	Budget      *RequestBudget
	Bucket      BudgetBucket
	Line        int
	Description string
}

// Evaluation contains a validated answer and its source-attributed trace.
type Evaluation struct {
	Answer map[string]any
	Trace  Trace
}

// Evaluate is the single live provider gateway for interpretation and runtime.
// Every dispatched request consumes a reservation, including uncertain failures.
// No automatic retries are made. This does not execute user source code.
func Evaluate(ctx context.Context, req EvaluationRequest) (Evaluation, error) {
	var out Evaluation
	if req.Budget == nil {
		return out, fmt.Errorf("provider request requires a budget")
	}
	questions := typesafe.Questions{"answer": req.Question}
	if req.Question.Type == questionBatch {
		var err error
		questions, err = decodeQuestions(req.Question.Criteria)
		if err != nil {
			return out, err
		}
	} else if err := validateQuestion(req.Question); err != nil {
		return out, err
	}
	reservation, err := req.Budget.Admit(ctx, req.Bucket)
	if err != nil {
		return out, err
	}
	defer reservation.Complete(0, false)
	env, _ := ctx.Value(environmentContextKey{}).(map[string]string)
	model := providerModel(ctx, req.Model)
	options := []typesafe.Option{typesafe.WithModel(model), typesafe.WithMaxRetries(0), typesafe.WithHTTPClient(&http.Client{Timeout: 30 * time.Second, Transport: validatingTransport{base: http.DefaultTransport, question: req.Question, usage: reservation.Complete}})}
	if v, ok := env["TYPESAFE_API_KEY"]; ok {
		options = append(options, typesafe.WithAPIKey(v))
	}
	if v, ok := env["TYPESAFE_BASE_URL"]; ok {
		options = append(options, typesafe.WithBaseURL(v))
	}
	client, err := typesafe.New(options...)
	if err != nil {
		return out, err
	}
	started := time.Now()
	res, err := client.SystemOne(ctx, typesafe.Request{State: req.State, Questions: questions, Model: model})
	if err != nil {
		return out, err
	}
	out.Answer = map[string]any{}
	for id, q := range questions {
		answer, ok := res.Answers[id]
		if !ok || answer.Type != q.Type {
			return Evaluation{}, fmt.Errorf("Jev returned a missing or wrong-kind answer for %q", id)
		}
		raw := map[string]any{"type": answer.Type, "noul": answer.Noul, "choice": answer.Choice, "score": answer.Score, "confidence": answer.Confidence, "probabilities": answer.Probabilities}
		normalized, err := normalizedAnswer(raw, q)
		if err != nil {
			return Evaluation{}, fmt.Errorf("answer %q: %w", id, err)
		}
		if req.Question.Type == questionBatch {
			out.Answer[id] = normalized
		} else {
			out.Answer = normalized
		}
	}
	out.Trace = Trace{Line: req.Line, Question: req.Description, Model: res.Model, Answer: out.Answer, Milliseconds: time.Since(started).Milliseconds(), InputTokens: res.Usage.InputTokens}
	return out, nil
}
