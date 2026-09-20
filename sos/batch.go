package sos

import (
	"encoding/json"
	"fmt"
	"strings"
	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

// questionBatch is an internal envelope. It is never sent as an API question.
const questionBatch = "sos-question-batch"

func validateQuestion(q typesafe.Question) error {
	if q.Instructions == nil {
		return fmt.Errorf("question requires instructions")
	}
	if s, ok := q.Instructions.(string); ok && strings.TrimSpace(s) == "" {
		return fmt.Errorf("question instructions must not be empty")
	}
	switch q.Type {
	case "noul":
		if q.Criteria != nil {
			c, ok := q.Criteria.(map[string]any)
			if !ok || len(c) != 2 || c["true"] == nil || c["false"] == nil {
				return fmt.Errorf("noul criteria require true and false descriptions")
			}
		}
	case "choice":
		c, ok := q.Criteria.(map[string]any)
		if !ok || len(c) < 2 {
			return fmt.Errorf("choice requires at least two named criteria")
		}
		for k := range c {
			if strings.TrimSpace(k) == "" {
				return fmt.Errorf("choice label must not be empty")
			}
		}
	case "score":
		c, ok := q.Criteria.([]any)
		if !ok || len(c) < 2 {
			return fmt.Errorf("score requires at least two levels")
		}
	default:
		return fmt.Errorf("unsupported question type %q", q.Type)
	}
	return nil
}

// decodeQuestions accepts data loaded from JSON or constructed inside SOS.
func decodeQuestions(value any) (typesafe.Questions, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err = json.Unmarshal(data, &raw); err != nil || len(raw) == 0 || len(raw) > 128 {
		return nil, fmt.Errorf("questions must be a record with 1 to 128 named questions")
	}
	out := typesafe.Questions{}
	for id, data := range raw {
		if strings.TrimSpace(id) == "" {
			return nil, fmt.Errorf("question ID must not be empty")
		}
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(data, &fields); err != nil {
			return nil, fmt.Errorf("question %q must be a record", id)
		}
		for key := range fields {
			if key != "type" && key != "instructions" && key != "criteria" {
				return nil, fmt.Errorf("question %q: unknown field %q", id, key)
			}
		}
		var q typesafe.Question
		if err = json.Unmarshal(data, &q); err != nil {
			return nil, fmt.Errorf("question %q: %w", id, err)
		}
		if err = validateQuestion(q); err != nil {
			return nil, fmt.Errorf("question %q: %w", id, err)
		}
		out[id] = q
	}
	return out, nil
}

func normalizedAnswer(a map[string]any, q typesafe.Question) (map[string]any, error) {
	if a["type"] != q.Type {
		return nil, fmt.Errorf("missing or wrong-kind answer")
	}
	var result map[string]any
	switch q.Type {
	case "noul":
		result = map[string]any{"p_yes": a["noul"]}
	case "choice":
		result = map[string]any{"value": a["choice"], "confidence": a["confidence"], "probabilities": a["probabilities"]}
	case "score":
		result = map[string]any{"expected": a["score"], "confidence": a["confidence"], "probabilities": a["probabilities"]}
	}
	if err := validateAnswerMap(result, q); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *runtime) evaluateBatch(s *Statement, m []string) error {
	state, err := r.eval(m[1], nil)
	if err != nil {
		return err
	}
	value, err := r.eval(m[2], nil)
	if err != nil {
		return err
	}
	questions, err := decodeQuestions(value)
	if err != nil {
		return err
	}
	answer, err := r.judgment(s.Line, state, typesafe.Question{Type: questionBatch, Criteria: questions}, "Question batch", r.opts.Model)
	if err == nil {
		r.env[m[3]] = answer
	}
	return err
}

func questionValue(q typesafe.Question) (any, error) {
	if err := validateQuestion(q); err != nil {
		return nil, err
	}
	value := map[string]any{"type": q.Type, "instructions": q.Instructions}
	if q.Criteria != nil {
		value["criteria"] = q.Criteria
	}
	return value, nil
}

func init() {
	stdRegistry["std/jev"] = map[string]NativeOp{
		"answer": {Name: "answer", Params: []NativeParam{{Name: "answers", Type: "any"}, {Name: "id", Type: "text"}}, Fn: func(a []any) (any, error) {
			answers, ok := a[0].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("answers must be a record")
			}
			return property(answers, a[1].(string))
		}},
		"noul": {Name: "noul", Params: []NativeParam{{Name: "instructions", Type: "any"}}, Fn: func(a []any) (any, error) { return questionValue(typesafe.Noul(a[0])) }},
		"choice": {Name: "choice", Params: []NativeParam{{Name: "instructions", Type: "any"}, {Name: "criteria", Type: "any"}}, Fn: func(a []any) (any, error) {
			return questionValue(typesafe.Question{Type: "choice", Instructions: a[0], Criteria: a[1]})
		}},
		"score": {Name: "score", Params: []NativeParam{{Name: "instructions", Type: "any"}, {Name: "levels", Type: "list"}}, Fn: func(a []any) (any, error) {
			return questionValue(typesafe.Question{Type: "score", Instructions: a[0], Criteria: a[1]})
		}},
		"questions": {Name: "questions", Params: []NativeParam{{Name: "entries", Type: "list"}}, Fn: func(a []any) (any, error) {
			out := map[string]any{}
			for _, value := range a[0].([]any) {
				entry, ok := value.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("question entry must be a record")
				}
				id, ok := entry["id"].(string)
				if !ok || strings.TrimSpace(id) == "" {
					return nil, fmt.Errorf("question entry requires a nonempty text id")
				}
				if len(entry) != 2 || entry["question"] == nil {
					return nil, fmt.Errorf("question entry requires only id and question")
				}
				if _, exists := out[id]; exists {
					return nil, fmt.Errorf("duplicate question ID %q", id)
				}
				out[id] = entry["question"]
			}
			if _, err := decodeQuestions(out); err != nil {
				return nil, err
			}
			return out, nil
		}},
	}
	for name, desc := range map[string]string{"answer": "Looks up a named answer; missing IDs are errors.", "noul": "Constructs a yes/no question without calling Jev.", "choice": "Constructs a named-choice question without calling Jev.", "score": "Constructs an ordered score question without calling Jev.", "questions": "Builds a named question record from id/question entries; rejects duplicate IDs."} {
		stdDocs["std/jev."+name] = stdDoc{"record", desc, []string{"pure"}}
	}
}
