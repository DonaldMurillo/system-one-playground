package typesafe

// Question is one typed question. Build them with Noul, Choice and Score.
// Instructions and criteria accept a string or any JSON-marshalable structure.
type Question struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions,omitempty"`
	Criteria     any    `json:"criteria,omitempty"`
}

// Questions are keyed by the id used to find each answer. Ids are for your code;
// the model never sees them, so put the full meaning in Instructions.
type Questions map[string]Question

// Noul asks a yes/no question and returns the probability of yes.
func Noul(instructions any) Question {
	return Question{Type: "noul", Instructions: instructions}
}

// NoulWith is Noul with explicit descriptions of the yes and no outcomes.
func NoulWith(instructions, yes, no any) Question {
	return Question{Type: "noul", Instructions: instructions, Criteria: map[string]any{"true": yes, "false": no}}
}

// Choice selects one label. A nil description leaves the label undescribed.
func Choice(instructions any, options map[string]any) Question {
	return Question{Type: "choice", Instructions: instructions, Criteria: options}
}

// Options builds undescribed Choice options from labels.
func Options(labels ...string) map[string]any {
	m := make(map[string]any, len(labels))
	for _, l := range labels {
		m[l] = nil
	}
	return m
}

// Score rates against ordered levels indexed from zero. Give at least two.
func Score(instructions any, levels ...any) Question {
	return Question{Type: "score", Instructions: instructions, Criteria: levels}
}
