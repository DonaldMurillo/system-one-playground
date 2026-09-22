package soslsp

import (
	"regexp"
	"strings"
)

// Canonical time forms from the time specification. The core keyword list and
// checker follow the runtime grammar; these editor surfaces describe the
// timing behavior that matters at the call site so buffers that use time
// statements get completion and hover before and while the runtime catches up.
type timeSentenceForm struct {
	// label is the completion label shown to the user.
	label string
	// insert is the snippet inserted on commit.
	insert string
	// trigger is the lowercase text a typed prefix must start to offer this
	// form (the "stream " prefix is stripped when the line already opens a
	// stream sentence).
	trigger string
	// hoverRE recognizes the written sentence for hover documentation.
	hoverRE *regexp.Regexp
	// hover documents the behavioral contract in markdown.
	hover string
}

var timeSentenceForms = []timeSentenceForm{
	{
		label:   "wait for 5 seconds",
		insert:  "wait for ${1:5 seconds}",
		trigger: "wait for",
		hoverRE: regexp.MustCompile(`^wait for\b`),
		hover: "Waits for a duration on the monotonic clock. Cancelable, and it " +
			"inherits the current context deadline, including any enclosing " +
			"`allow at most` scope.",
	},
	{
		label:   "wait until deadline",
		insert:  "wait until ${1:deadline}",
		trigger: "wait until",
		hoverRE: regexp.MustCompile(`^wait until\b`),
		hover: "Resolves the timestamp once, then waits monotonically for the " +
			"remaining duration, so wall-clock adjustments cannot restart it. " +
			"Waiting until a past timestamp completes immediately.",
	},
	{
		label:   "stream one tick after 30 seconds called timer",
		insert:  "one tick after ${1:30 seconds} called ${2:timer}",
		trigger: "one tick",
		hoverRE: regexp.MustCompile(`^stream one tick after\b`),
		hover: "A one-shot timer stream: it emits exactly one `TimeTick` and " +
			"completes. Unlike `wait`, it can be transformed, canceled " +
			"independently, inspected, and selected alongside other streams.",
	},
	{
		label:   "stream a tick every 10 seconds called ticks",
		insert:  "a tick every ${1:10 seconds} called ${2:ticks}",
		trigger: "a tick",
		hoverRE: regexp.MustCompile(`^stream a tick every\b`),
		hover: "A repeating timer anchored to its original schedule: handling " +
			"duration never drifts the next tick. The default missed-tick " +
			"policy combines missed ticks (`missed` counts the schedule " +
			"positions folded into one delivery); `skipping missed ticks` and " +
			"`catching up at most N ticks` are explicit alternatives.",
	},
	{
		label:   "stream a tick now and every 10 seconds called ticks",
		insert:  "a tick now and every ${1:10 seconds} called ${2:ticks}",
		trigger: "a tick",
		hoverRE: regexp.MustCompile(`^stream a tick now and every\b`),
		hover: "Like a repeating timer, but the first tick fires immediately " +
			"instead of after one complete interval.",
	},
	{
		label:   "stream scheduled times every weekday at 9:00 in time zone \"UTC\" called ticks",
		insert:  "scheduled times\n  every ${1:weekday} at ${2:9:00}\n  in time zone \"${3:UTC}\"\n  called ${4:ticks}",
		trigger: "scheduled",
		hoverRE: regexp.MustCompile(`^stream scheduled times\b`),
		hover: "A calendar schedule stream. Every calendar schedule requires a " +
			"named IANA time zone; daylight-saving policy (`skip` or `use the " +
			"next valid time` for nonexistent local times) is schedule " +
			"metadata and appears in traces. Without durable progress the " +
			"stream starts at the next occurrence after it opens.",
	},
	{
		label:   "allow at most 30 seconds for:",
		insert:  "allow at most ${1:30 seconds} for:",
		trigger: "allow at",
		hoverRE: regexp.MustCompile(`^allow at most\b`),
		hover: "A scoped deadline measured from block entry. Everything created " +
			"inside inherits it and the duration never resets per statement. " +
			"Expiry cancels the child context, stops owned streams and child " +
			"processes, and fails with `DeadlineExceeded` unless handled. " +
			"Nested blocks use the earliest effective deadline.",
	},
	{
		label:   "advance test time by 10 minutes",
		insert:  "advance test time by ${1:10 minutes}",
		trigger: "advance",
		hoverRE: regexp.MustCompile(`^advance test time by\b`),
		hover: "Advances the deterministic virtual clock without sleeping, " +
			"running due timers in scheduled-time then creation order. " +
			"Available only in test harnesses, never ordinary production " +
			"programs.",
	},
}

// timeCompletions offers canonical time sentence forms for the typed prefix.
// Stream-opened timer forms (labels starting with "stream ") complete the
// words after "stream " only on a line that already opens a stream sentence,
// mirroring streamCompletionForm.
func timeCompletions(lineContext, prefix string, edit func(string) map[string]any) []map[string]any {
	if prefix == "" {
		return nil
	}
	prefix = strings.ToLower(prefix)
	onStreamLine := strings.HasPrefix(lineContext, "stream ")
	items := make([]map[string]any, 0, 2)
	for _, form := range timeSentenceForms {
		label, insert := form.label, form.insert
		if strings.HasPrefix(form.label, "stream ") {
			if !onStreamLine {
				continue
			}
			// The line already provides "stream "; complete the remainder.
			label = strings.TrimPrefix(form.label, "stream ")
		}
		if !strings.HasPrefix(strings.ToLower(label), prefix) && !strings.HasPrefix(form.trigger, prefix) {
			continue
		}
		item := map[string]any{
			"label":            label,
			"kind":             14,
			"detail":           "SysOneScript time form",
			"documentation":    map[string]any{"kind": "markdown", "value": form.hover},
			"sortText":         "~" + label,
			"filterText":       label,
			"insertTextFormat": 2,
			"textEdit":         edit(insert),
		}
		items = append(items, item)
	}
	return items
}

// timeHover documents a written time sentence, or reports false.
func timeHover(line string) (string, bool) {
	trimmed := strings.TrimSpace(stripLineComment(line))
	for _, form := range timeSentenceForms {
		if form.hoverRE.MatchString(trimmed) {
			return "```sos\n" + trimmed + "\n```\n\n" + form.hover, true
		}
	}
	return "", false
}
