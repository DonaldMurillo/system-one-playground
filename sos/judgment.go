package sos

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

type record struct {
	Failure map[string]any `json:"failure,omitempty"`
	Path    string         `json:"path,omitempty"`
	Key     string         `json:"key"`
	Trace   Trace          `json:"trace"`
}

func (r *runtime) judgePredicate(s *Statement, predicate string, item any) (any, error) {
	threshold := 0.8
	question := strings.TrimSpace(strings.TrimPrefix(predicate, "jev"))
	state := item
	model := r.opts.Model
	uncertain := "discard"
	if question == ":" {
		question = ""
		for _, c := range s.Body {
			switch c.Kind {
			case "ask":
				v, e := r.text(match(c.Kind, c.Text)[1])
				if e != nil {
					return nil, e
				}
				question = v
			case "using":
				fields := strings.Split(match(c.Kind, c.Text)[1], ",")
				m := map[string]any{}
				for _, field := range fields {
					field = strings.TrimSpace(field)
					v, e := r.eval(field, item)
					if e != nil {
						return nil, e
					}
					m[field] = v
				}
				state = m
			case "accept":
				v, e := r.eval(match(c.Kind, c.Text)[1], nil)
				if e != nil {
					return nil, e
				}
				n, ok := number(v)
				if !ok || n <= 0.5 || n > 1 {
					return nil, fmt.Errorf("probability threshold must be greater than 0.5 and at most 1")
				}
				threshold = n
			case "model":
				v, e := r.text(match(c.Kind, c.Text)[1])
				if e != nil {
					return nil, e
				}
				model = v
			case "handler":
				m := match(c.Kind, c.Text)
				if m[1] == "uncertain" {
					uncertain = m[2]
				}
			}
		}
	} else {
		v, e := r.eval(question, item)
		if e != nil {
			return nil, e
		}
		var ok bool
		question, ok = v.(string)
		if !ok {
			return nil, fmt.Errorf("jev requires a quoted question")
		}
	}
	if question == "" {
		return nil, fmt.Errorf("jev requires ask with a question")
	}
	answer, e := r.judgment(s.Line, state, typesafe.Noul(question), question, model, func(t *Trace) {
		t.Item = item
		p := t.Answer.(map[string]any)["p_yes"].(float64)
		switch {
		case p >= threshold:
			t.Decision = "keep"
			t.Reason = fmt.Sprintf("Probability %.3f meets acceptance threshold %.3f.", p, threshold)
		case p <= 1-threshold:
			t.Decision = "discard"
			t.Reason = fmt.Sprintf("Probability %.3f is at or below rejection threshold %.3f.", p, 1-threshold)
		default:
			t.Decision = uncertain
			t.Reason = fmt.Sprintf("Probability %.3f is uncertain between %.3f and %.3f; policy: %s.", p, 1-threshold, threshold, uncertain)
		}
	})
	if e != nil {
		return nil, e
	}
	p := answer["p_yes"].(float64)
	if p >= threshold {
		return true, nil
	}
	if p <= 1-threshold {
		return false, nil
	}
	switch uncertain {
	case "discard":
		return false, nil
	case "keep":
		return true, nil
	case "stop":
		return nil, fmt.Errorf("uncertain judgment (p_yes=%g)", p)
	default:
		return nil, fmt.Errorf("on uncertain expects keep, discard, or stop")
	}
}
func (r *runtime) classify(s *Statement, m []string) error {
	state, e := r.eval(m[1], nil)
	if e != nil {
		return e
	}
	if !strings.HasPrefix(m[2], "jev ") {
		return fmt.Errorf("semantic judgments require by jev followed by a question")
	}
	question, e := r.text(strings.TrimPrefix(m[2], "jev "))
	if e != nil {
		return e
	}
	options := map[string]any{}
	for _, c := range s.Body {
		if c.Kind != "choice" {
			continue
		}
		tokens, e := lex(c.Text)
		if e != nil || len(tokens) != 3 {
			return fmt.Errorf("invalid choice declaration")
		}
		if _, exists := options[tokens[0].text]; exists {
			return fmt.Errorf("duplicate choice %q", tokens[0].text)
		}
		options[tokens[0].text] = tokens[2].text
	}
	if s.Kind == "judge" {
		a, e := r.judgment(s.Line, state, typesafe.Noul(question), question, r.opts.Model)
		if e == nil {
			r.env[m[3]] = a
		}
		return e
	}
	if len(options) < 2 {
		return fmt.Errorf("classify needs at least two choices")
	}
	q := typesafe.Choice(question, options)
	if s.Kind == "score" {
		levels := make([]any, len(options))
		for k, v := range options {
			i, err := strconv.Atoi(k)
			if err != nil || i < 0 || i >= len(levels) {
				return fmt.Errorf("score levels must be numbered consecutively from 0")
			}
			levels[i] = v
		}
		q = typesafe.Score(question, levels...)
	}
	a, e := r.judgment(s.Line, state, q, question, r.opts.Model)
	if e == nil {
		r.env[m[3]] = a
	}
	return e
}
func (r *runtime) judgment(line int, state any, q typesafe.Question, question, model string, enrich ...func(*Trace)) (map[string]any, error) {
	if e := r.tick(); e != nil {
		return nil, e
	}
	model = providerModel(r.ctx, model)
	payload, e := json.Marshal(struct {
		Source   string
		Line     int
		State    any
		Question typesafe.Question
		Model    string
		Path     string `json:",omitempty"`
	}{r.p.Source, line, state, q, model, r.logicalPath})
	if e != nil {
		return nil, e
	}
	sum := sha256.Sum256(payload)
	key := hex.EncodeToString(sum[:])
	if r.opts.Replay != "" {
		if r.replayIndex >= len(r.replay) {
			return nil, &ReplayIntegrityError{fmt.Sprintf("replay has no answer for line %d", line)}
		}
		rec := r.replay[r.replayIndex]
		if rec.Key != key {
			return nil, &ReplayIntegrityError{fmt.Sprintf("replay mismatch at line %d: source, state, question, or model changed (requested model %q; recorded response model %q)", line, model, rec.Trace.Model)}
		}
		r.replayIndex++
		if rec.Failure != nil {
			message, ok := rec.Failure["message"].(string)
			kind, kindOK := rec.Failure["kind"].(string)
			if !ok || message == "" || !kindOK || kind == "" {
				return nil, &ReplayIntegrityError{"invalid recorded failure"}
			}
			return nil, &recordedFailure{rec.Failure}
		}
		a, ok := rec.Trace.Answer.(map[string]any)
		if !ok {
			return nil, &ReplayIntegrityError{"invalid replay answer"}
		}
		if e = validateAnswerMap(a, q); e != nil {
			return nil, &ReplayIntegrityError{fmt.Sprintf("invalid replay: %v", e)}
		}
		tr := rec.Trace
		tr.Replay = true
		for _, f := range enrich {
			f(&tr)
		}
		r.trace(tr)
		return a, nil
	}
	if r.opts.Config.Runtime == "deny" {
		return nil, fmt.Errorf("runtime Jev judgment denied by configuration")
	}
	if used := r.shared.calls.Add(1); used > int64(r.opts.MaxCalls) {
		return nil, &BudgetError{Bucket: BudgetRuntime, Used: r.opts.MaxCalls, Limit: r.opts.MaxCalls}
	}
	evaluated, e := Evaluate(r.ctx, EvaluationRequest{State: state, Question: q, Model: model, Budget: r.opts.Budget, Bucket: BudgetRuntime, Line: line, Description: question})
	if e != nil {
		if !fatalParallel(e) {
			r.recording = append(r.recording, record{Key: key, Path: r.logicalPath, Failure: FailureValue(e)})
		}
		return nil, e
	}
	a, tr := evaluated.Answer, evaluated.Trace
	for _, f := range enrich {
		f(&tr)
	}

	r.trace(tr)
	r.recording = append(r.recording, record{Key: key, Trace: tr, Path: r.logicalPath})
	return a, nil
}
func (r *runtime) trace(t Trace) {
	r.result.Traces = append(r.result.Traces, t)
	if r.opts.OnTrace != nil {
		r.shared.traceMu.Lock()
		r.opts.OnTrace(t)
		r.shared.traceMu.Unlock()
	}
}
func probability(v any) bool {
	n, ok := number(v)
	return ok && !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && n <= 1
}
func validateAnswerMap(a map[string]any, q typesafe.Question) error {
	if q.Type == questionBatch {
		questions, err := decodeQuestions(q.Criteria)
		if err != nil {
			return err
		}
		if len(a) != len(questions) {
			return fmt.Errorf("batch answer count mismatch")
		}
		for id, question := range questions {
			answer, ok := a[id].(map[string]any)
			if !ok {
				return fmt.Errorf("missing batch answer %q", id)
			}
			if err := validateAnswerMap(answer, question); err != nil {
				return fmt.Errorf("answer %q: %w", id, err)
			}
		}
		return nil
	}

	if q.Type == "noul" {
		if !probability(a["p_yes"]) {
			return fmt.Errorf("invalid Noul probability")
		}
		return nil
	}
	if !probability(a["confidence"]) {
		return fmt.Errorf("invalid confidence")
	}
	if q.Type == "choice" {
		label, ok := a["value"].(string)
		if !ok {
			return fmt.Errorf("missing choice")
		}
		options := q.Criteria.(map[string]any)
		if _, ok = options[label]; !ok {
			return fmt.Errorf("unknown choice %q", label)
		}
	}
	raw, e := json.Marshal(a["probabilities"])
	if e != nil {
		return e
	}
	var distribution map[string]any
	if e = json.Unmarshal(raw, &distribution); e != nil || len(distribution) == 0 {
		return fmt.Errorf("missing probability distribution")
	}
	expected := map[string]bool{}
	if q.Type == "choice" {
		for key := range q.Criteria.(map[string]any) {
			expected[key] = true
		}
	} else {
		levels := q.Criteria.([]any)
		for i := range levels {
			expected[strconv.Itoa(i)] = true
		}
		n, ok := number(a["expected"])
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > float64(len(levels)-1) {
			return fmt.Errorf("invalid score expectation")
		}
	}
	if len(distribution) != len(expected) {
		return fmt.Errorf("incomplete probability distribution")
	}
	sum := 0.
	for key, v := range distribution {
		if !expected[key] || !probability(v) {
			return fmt.Errorf("invalid probability for %s", key)
		}
		n, _ := number(v)
		sum += n
	}
	if math.Abs(sum-1) > 0.02 {
		return fmt.Errorf("probabilities do not sum to one")
	}

	return nil
}

// Validate required JSON fields before the existing client decodes into zero-value Go fields.
type validatingTransport struct {
	base     http.RoundTripper
	question typesafe.Question
	usage    func(int, bool)
}

func (v validatingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	res, e := v.base.RoundTrip(req)
	if e != nil {
		return nil, e
	}
	if res.StatusCode >= 300 {
		return res, nil
	}
	defer res.Body.Close()
	data, e := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if e != nil {
		return nil, e
	}
	var raw struct {
		Answers map[string]map[string]any `json:"answers"`
		Usage   struct {
			InputTokens *int `json:"input_tokens"`
		} `json:"usage"`
	}
	if e = json.Unmarshal(data, &raw); e != nil {
		return nil, fmt.Errorf("invalid Jev JSON: %w", e)
	}
	if v.usage != nil && raw.Usage.InputTokens != nil {
		v.usage(*raw.Usage.InputTokens, *raw.Usage.InputTokens >= 0)
	}
	questions := typesafe.Questions{"answer": v.question}
	if v.question.Type == questionBatch {
		questions, e = decodeQuestions(v.question.Criteria)
		if e != nil {
			return nil, e
		}
	}
	if len(raw.Answers) != len(questions) {
		return nil, fmt.Errorf("Jev response answer count mismatch")
	}
	for id, q := range questions {
		a, ok := raw.Answers[id]
		if !ok {
			return nil, fmt.Errorf("Jev response missing answer %q", id)
		}
		if _, err := normalizedAnswer(a, q); err != nil {
			return nil, fmt.Errorf("answer %q: %w", id, err)
		}
	}
	res.Body = io.NopCloser(bytes.NewReader(data))
	return res, nil
}
