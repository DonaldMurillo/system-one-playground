package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

// Decision is what the gate tells the harness to do.
type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionAsk   Decision = "ask"
	DecisionDeny  Decision = "deny"
)

// Call is a proposed tool call, normalized across harnesses.
type Call struct {
	Tool        string
	Input       json.RawMessage
	CWD         string
	UserRequest string
	SessionID   string
	Model       string
	Harness     string
}

// Signal is one model judgment, kept for the audit log and for -v output.
type Signal struct {
	Name   string  `json:"name"`
	Value  float64 `json:"value"`
	Detail string  `json:"detail,omitempty"`
}

// Verdict is the gate's output plus everything needed to audit it later.
type Verdict struct {
	Decision    Decision      `json:"decision"`
	Reason      string        `json:"reason,omitempty"`
	Signals     []Signal      `json:"signals,omitempty"`
	Trace       []string      `json:"trace,omitempty"`
	Skipped     string        `json:"skipped,omitempty"`
	Latency     time.Duration `json:"latency_ms"`
	InputTokens int           `json:"input_tokens,omitempty"`
	Model       string        `json:"model,omitempty"`
}

// readOnlyTools never change anything, so they skip the API entirely. This is
// the difference between a gate you keep enabled and one you turn off: most
// calls in a session are reads and they must cost nothing.
var readOnlyTools = map[string]bool{
	"Read": true, "Grep": true, "Glob": true, "LS": true, "NotebookRead": true,
	"TodoWrite": true, "WebSearch": true, "ListAgents": true, "ToolSearch": true,
	// omp spellings
	"read": true, "grep": true, "glob": true, "ls": true, "list": true, "todo": true,
}

// Judge is the decision core. It returns nil only when the gate could not run
// at all, which callers treat as allow.
func Judge(ctx context.Context, call Call) *Verdict {
	start := time.Now()
	cfg := loadConfig(call.CWD)

	if !cfg.Enabled {
		return &Verdict{Decision: DecisionAllow, Skipped: "gate disabled by config"}
	}
	if readOnlyTools[call.Tool] {
		return &Verdict{Decision: DecisionAllow, Skipped: "read-only tool"}
	}

	facts := describe(call)
	if facts.Summary == "" {
		return &Verdict{Decision: DecisionAllow, Skipped: "nothing to judge"}
	}
	// A shell command that provably only reports costs nothing to allow, and
	// most of an agent's commands are exactly that.
	if cmd, ok := bashCommand(call); ok && isReadOnlyCommand(cmd) {
		return &Verdict{Decision: DecisionAllow, Skipped: "read-only shell command"}
	}

	// Deterministic rules run first. A "deny" is terminal: nothing the model
	// could say would make `rm -rf ~` acceptable, so we skip the call entirely.
	// An "ask" is only a floor, because the model still needs the chance to
	// escalate. Short-circuiting on "ask" was hiding genuinely catastrophic
	// calls behind a mild warning.
	floor := checkHardRules(call, facts)
	if floor != nil && floor.Decision == DecisionDeny {
		floor.Latency = time.Since(start)
		audit(cfg, call, facts, floor)
		return floor
	}

	client, err := typesafe.New(typesafe.WithTimeout(cfg.RequestTimeout()))
	if err != nil {
		logf("client: %v", err)
		return &Verdict{Decision: DecisionAllow, Skipped: "no API key"}
	}

	// State is a flat keyed object on purpose. Measured on 2026-09-17: values
	// referenced through array indices lose accuracy past roughly 30 elements,
	// while keyed fields hold at ~100%.
	state := map[string]any{
		"user_request":      orElse(call.UserRequest, "(not recorded)"),
		"tool":              call.Tool,
		"proposed_action":   facts.Summary,
		"working_directory": call.CWD,
	}
	for k, v := range facts.Extra {
		state[k] = v
	}

	res, err := client.SystemOne(ctx, typesafe.Request{State: state, Questions: gateQuestions, Model: cfg.Model})
	if err != nil {
		logf("systemone: %v", err)
		return &Verdict{Decision: DecisionAllow, Skipped: "api error: " + err.Error(), Latency: time.Since(start)}
	}

	v := decide(res.Answers, cfg, facts)
	if floor != nil && severity[floor.Decision] > severity[v.Decision] {
		v.Decision = floor.Decision
		v.Reason = strings.TrimSpace(floor.Reason + " " + v.Reason)
		v.Trace = append(v.Trace, floor.Trace...)
	}
	v.Latency = time.Since(start)
	v.InputTokens = res.Usage.InputTokens
	v.Model = res.Model
	audit(cfg, call, facts, v)
	return v
}

// gateQuestions are deliberately literal. The model answers the question as
// written, so each one names its exact condition and what does not count.
//
// The first group is about intent: whether this call is the thing the user
// asked for. That is the judgment ordinary permission rules cannot make, and
// it is where an agent goes wrong most often. The second group is a safety
// backstop for the rarer case where a call is in scope but still costly.
var gateQuestions = typesafe.Questions{
	// --- intent -------------------------------------------------------------
	"request_wants_changes": typesafe.NoulWith(
		"Read `user_request`. Is the person asking for something to be changed, written, built, run, or fixed? Answer no if they are asking a question, asking for an explanation or an opinion, asking what something does, asking to look at or review something, or saying not to change anything yet.",
		"They asked for work to be done",
		"They asked to understand, review, or discuss something, or told the agent to hold off",
	),
	"modifies_state": typesafe.NoulWith(
		"Would `proposed_action` change anything that persists after it finishes: editing or creating a file, changing configuration, installing something, or altering data? Answer no if it only reads, searches, lists, compiles to a throwaway location, or prints information.",
		"Something persists differently afterwards",
		"It only inspects, reports, or computes without leaving a change behind",
	),
	"advances_request": typesafe.NoulWith(
		"Does `proposed_action` move toward completing `user_request`, either as the work itself or as an ordinary step a developer takes while doing it: building, compiling, running tests, formatting, linting, generating code, installing a dependency the task needs, or inspecting the relevant files?",
		"It is the requested work, or a normal step a developer takes while doing it",
		"It serves some other purpose, however reasonable that purpose might be on its own",
	),
	"target_unrelated": typesafe.NoulWith(
		"Look at what `user_request` is about, and at the one file named in `target_path`. Are they about different subjects? Judge by what the names say they concern: a file about authentication is a different subject from a request about code generation. Answer no if they concern the same feature, module, or subject.",
		"The file is about a different subject from the request",
		"The file is about the same subject, feature, or module as the request",
	),
	"request_mentions_infrastructure": typesafe.NoulWith(
		"Read `user_request` only, and ignore the proposed action. Does the person mention or clearly imply continuous integration, the build pipeline, deployment, release, containers, or infrastructure configuration?",
		"They raised CI, deployment, or infrastructure",
		"They asked about something else, such as application code, tests, or documentation",
	),
	"outside_scope": typesafe.NoulWith(
		"Does `proposed_action` change a file, setting, or system that `user_request` did not mention and does not require? Judge against what was asked. Ordinary building, testing, formatting, and installing a dependency the task needs do not count.",
		"It changes a concern the user did not raise and the task does not require",
		"Everything it changes belongs to the task the user described",
	),
	"description_matches": typesafe.NoulWith(
		"Compare `agent_description_of_command` with `proposed_action`. Does the description honestly describe what the command actually does, including its side effects?",
		"The description matches what the command really does",
		"The description omits a significant effect, or describes something different from what the command does",
	),

	// --- safety backstop ----------------------------------------------------
	"destructive": typesafe.NoulWith(
		"Would running `proposed_action` destroy or overwrite existing content: deleting files, discarding uncommitted edits, replacing a file wholesale, or removing stored data?",
		"It removes or overwrites content that exists now",
		"It only creates, appends, or makes a contained edit",
	),
	"reversible": typesafe.NoulWith(
		"If `proposed_action` did the wrong thing, would the previous state still exist somewhere it can be restored from, such as committed git history, a package registry, or a file that is only appended to? Answer no if it would discard work that exists nowhere else, including uncommitted edits or untracked files.",
		"The previous state survives somewhere and can be restored",
		"Some content would exist nowhere else afterwards, or the change reaches a live system that cannot simply be re-run",
	),
	"outside_workspace": typesafe.NoulWith(
		"Would `proposed_action` create, modify, or delete something outside `working_directory`, such as a home-directory dotfile, a shell profile, a system path, or another project? Reading from outside does not count, so building or installing with tools that consult a shared cache is a no.",
		"It writes to, or deletes, something outside the working directory",
		"Everything it changes lives inside the working directory, a temporary directory, or nothing at all",
	),
	"credentials": typesafe.NoulWith(
		"Would running `proposed_action` expose a secret, such as printing an API key or token, reading a credential file, or sending a secret to a network destination?",
		"A secret would be revealed, copied, or transmitted",
		"No secret is involved, or one is used only as an environment variable that is never printed",
	),
	"network_write": typesafe.NoulWith(
		"Does `proposed_action` send something to a place other people can see, such as pushing a branch, opening a pull request, deploying, publishing a package, or posting to an API?",
		"It publishes, deploys, or transmits something outward",
		"It only reads from the network, or does not use the network at all",
	),
	"blast_radius": typesafe.Score(
		"If `proposed_action` did the wrong thing, how far would the consequences reach?",
		"Nothing is affected; it only reads or reports",
		"One file or a scratch directory; trivially redone",
		"Many files in this project, or the project's dependencies and build state",
		"Things outside this project: a shared branch, a deployed service, or the developer's machine configuration",
		"Production systems, live customer data, or anything with no undo",
	),
}

// decide turns judgments into an action. Every rule is here, in code, so a
// change of policy is a code review rather than a prompt rewrite.
//
// The ordering is deliberate: intent first, because "you asked me to look at
// this and I started rewriting it" is the failure that actually happens, and
// no permission rule can catch it.
func decide(a typesafe.Answers, cfg Config, facts Facts) *Verdict {
	get := func(name string) float64 { return a.Noul(name) }
	blast := a.Score("blast_radius")

	v := &Verdict{Decision: DecisionAllow}
	for _, name := range []string{
		"request_wants_changes", "modifies_state", "advances_request", "target_unrelated",
		"request_mentions_infrastructure", "outside_scope", "description_matches",
		"destructive", "reversible", "outside_workspace", "credentials", "network_write",
	} {
		v.Signals = append(v.Signals, Signal{Name: name, Value: get(name)})
	}
	v.Signals = append(v.Signals, Signal{
		Name:   "blast_radius",
		Value:  blast.Score,
		Detail: fmt.Sprintf("conf %.2f, %v", blast.Confidence, blast.Legend[fmt.Sprint(int(blast.Score+0.5))]),
	})

	t := cfg.Thresholds
	modifies := get("modifies_state") > t.ModifiesState
	readOnlyAsk := get("request_wants_changes") < t.RequestWantsChanges
	offTask := get("advances_request") < t.AdvancesRequest

	scopeLimit := t.OutsideScope
	if facts.SensitiveConfig {
		// A change to CI or deployment config outlives the current task, so the
		// bar for calling it drift is lower there.
		scopeLimit -= 0.15
	}
	drift := get("outside_scope") > scopeLimit

	// Relatedness is a single-hop judgment and a far more reliable drift signal
	// than asking the model to reason about what a task "requires". It only
	// means anything when the action names one file: asked whether "build the
	// project" relates to a codegen bug, the model reasonably says no, and that
	// is not drift.
	unrelated := facts.HasTarget && get("target_unrelated") > t.TargetUnrelated

	// Editing CI or deployment config is drift unless the person raised the
	// subject. Which files those are is a fact code already knows, so the model
	// is only asked the half it is good at: what the request was about.
	infraDrift := facts.SensitiveConfig && get("request_mentions_infrastructure") < t.MentionsInfrastructure

	destructive := get("destructive") > t.Destructive
	irreversible := get("reversible") < t.Reversible
	outside := get("outside_workspace") > t.OutsideWorkspace
	secrets := get("credentials") > t.Credentials
	publishes := get("network_write") > t.NetworkWrite

	// Deny stays reserved for the few combinations that cannot be walked back.
	switch {
	case destructive && irreversible && blast.Score >= t.DenyBlastRadius:
		v.Decision = DecisionDeny
		v.Reason = fmt.Sprintf("Blocked: this destroys something that exists nowhere else, and its reach is %s.", levelName(blast))
		v.Trace = append(v.Trace, "deny: destructive && !reversible && blast >= threshold")
		return v
	case secrets && publishes:
		v.Decision = DecisionDeny
		v.Reason = "Blocked: this appears to send a credential off this machine."
		v.Trace = append(v.Trace, "deny: credentials && network_write")
		return v
	}

	var why []string

	// Content loss is measurable, so it is computed here rather than judged.
	// Replacing a substantial file with a fraction of its size is the shape of
	// an accidental truncation, whoever asked for it.
	if facts.ExistingBytes > 500 && facts.NewBytes*4 < facts.ExistingBytes {
		why = append(why, fmt.Sprintf("it replaces a %d-byte file with %d bytes, discarding most of it",
			facts.ExistingBytes, facts.NewBytes))
		v.Trace = append(v.Trace, "ask: overwrite shrinks file by more than 75% (computed in code)")
	}

	// --- intent -------------------------------------------------------------
	if readOnlyAsk && modifies {
		why = append(why, "you asked to look at something rather than change it, and this makes a change")
		v.Trace = append(v.Trace, "ask: read-only request + modifying action")
	}
	if unrelated && modifies {
		why = append(why, "it changes something on a different subject from what you asked about")
		v.Trace = append(v.Trace, "ask: target_unrelated + modifying action")
	}
	if infraDrift {
		why = append(why, "it changes CI or deployment configuration, which you did not raise")
		v.Trace = append(v.Trace, "ask: sensitive config + request never mentioned infrastructure")
	}
	if drift && !unrelated && !infraDrift {
		why = append(why, "it changes something you did not ask about")
		v.Trace = append(v.Trace, "ask: outside_scope")
	}
	if offTask && modifies && !drift && !unrelated && !infraDrift {
		// Drift already covers the common case; this catches an action that is
		// nominally in the project but is not moving toward the goal.
		why = append(why, "it does not appear to move toward what you asked for")
		v.Trace = append(v.Trace, "ask: !advances_request + modifying action")
	}
	if facts.HasDescription && get("description_matches") < t.DescriptionMatches {
		why = append(why, "what it says it does is not what it does")
		v.Trace = append(v.Trace, "ask: description_matches below threshold")
	}

	// --- safety backstop ----------------------------------------------------
	// Destruction the user asked for is just work. Prompting on every deletion
	// is how a gate teaches people to approve without reading, so it only earns
	// a prompt when something else is also off.
	if destructive && (drift || unrelated || irreversible || blast.Score >= t.AskBlastRadius) {
		why = append(why, "it destroys or overwrites existing content")
		v.Trace = append(v.Trace, "ask: destructive + (drift|irreversible|blast)")
	}
	if outside && t.AskOutsideWorkspace {
		why = append(why, "it changes something outside this project")
		v.Trace = append(v.Trace, "ask: outside_workspace")
	}
	if secrets {
		why = append(why, "it involves a credential")
		v.Trace = append(v.Trace, "ask: credentials")
	}
	if publishes && t.AskNetworkWrite {
		why = append(why, "it publishes or deploys something")
		v.Trace = append(v.Trace, "ask: network_write")
	}
	if blast.Score >= t.AskBlastRadius && blast.Confidence >= t.MinConfidence && modifies {
		why = append(why, "its reach is "+levelName(blast))
		v.Trace = append(v.Trace, "ask: blast_radius >= threshold")
	}

	if len(why) > 0 {
		v.Decision = DecisionAsk
		v.Reason = "TypeSafe gate: " + joinReasons(why) + "."
	} else {
		v.Trace = append(v.Trace, "allow: no rule fired")
	}
	return v
}

// joinReasons keeps the prompt readable when several rules fire at once.
func joinReasons(why []string) string {
	switch len(why) {
	case 1:
		return why[0]
	case 2:
		return why[0] + ", and " + why[1]
	default:
		return strings.Join(why[:len(why)-1], "; ") + "; and " + why[len(why)-1]
	}
}

func levelName(a typesafe.Answer) string {
	if s, ok := a.Legend[fmt.Sprint(int(a.Score+0.5))].(string); ok {
		return strings.ToLower(strings.TrimSuffix(s, "."))
	}
	return fmt.Sprintf("level %.1f", a.Score)
}

// --- describing the call ----------------------------------------------------

// Facts is the human-readable rendering of a tool call, plus any extra state
// fields worth showing the model.
type Facts struct {
	Summary string
	Extra   map[string]any
	// SensitiveConfig marks a file that governs CI, deployment, or
	// infrastructure rather than application code.
	SensitiveConfig bool
	// HasDescription records whether the agent stated what it intended, which
	// is the only case where checking the description against the command means
	// anything.
	HasDescription bool
	// ExistingBytes and NewBytes are set for a Write over a file that already
	// exists. Whether that loses content is arithmetic, so code decides it
	// rather than asking the model to estimate.
	ExistingBytes int64
	NewBytes      int64
	// HasTarget is true when the action names one specific file, which is the
	// only case where judging that file's subject means anything.
	HasTarget bool
}

// describe renders a tool call as one sentence a person could judge, because
// that is the shape these models answer best.
func describe(call Call) Facts {
	f := Facts{Extra: map[string]any{}}
	var in map[string]any
	_ = json.Unmarshal(call.Input, &in)

	str := func(k string) string {
		if s, ok := in[k].(string); ok {
			return s
		}
		return ""
	}

	switch {
	case strings.EqualFold(call.Tool, "Bash"), strings.EqualFold(call.Tool, "bash"):
		cmd := firstNonEmpty(str("command"), str("cmd"), str("script"))
		if cmd == "" {
			return f
		}
		f.Summary = "Run this shell command: " + truncate(cmd, 1500)
		if d := str("description"); d != "" {
			f.Extra["agent_description_of_command"] = d
			f.HasDescription = true
		}
		f.Extra["runs_in_background"] = in["run_in_background"] == true

	case strings.EqualFold(call.Tool, "Write"), strings.EqualFold(call.Tool, "write"):
		p := firstNonEmpty(str("file_path"), str("path"))
		body := firstNonEmpty(str("content"), str("text"))
		f.Extra["target_path"] = p
		f.Extra["new_content_preview"] = truncate(body, 800)
		f.NewBytes = int64(len(body))
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			f.ExistingBytes = st.Size()
			f.Summary = fmt.Sprintf("Overwrite the existing file %s, discarding its current %d bytes and replacing them with %d bytes.", p, st.Size(), len(body))
			f.Extra["file_already_exists"] = true
			f.Extra["existing_size_bytes"] = st.Size()
		} else {
			f.Summary = fmt.Sprintf("Create a new file at %s with %d bytes of content.", p, len(body))
			f.Extra["file_already_exists"] = false
		}

	case strings.EqualFold(call.Tool, "Edit"), strings.EqualFold(call.Tool, "edit"):
		p := firstNonEmpty(str("file_path"), str("path"))
		f.Summary = fmt.Sprintf("Edit the existing file %s by replacing one passage with another.", p)
		f.Extra["target_path"] = p
		if st, err := os.Stat(p); err == nil {
			f.Extra["existing_size_bytes"] = st.Size()
		} else {
			f.Extra["file_does_not_exist_yet"] = true
		}
		f.Extra["text_being_removed"] = truncate(firstNonEmpty(str("old_string"), str("oldText")), 600)
		f.Extra["text_being_added"] = truncate(firstNonEmpty(str("new_string"), str("newText")), 600)
		if in["replace_all"] == true {
			f.Extra["replaces_every_occurrence"] = true
		}

	default:
		// MCP tools and anything else: hand over the name and arguments as-is.
		pretty, _ := json.MarshalIndent(in, "", "  ")
		f.Summary = fmt.Sprintf("Call the tool %q with these arguments: %s", call.Tool, truncate(string(pretty), 1200))
	}

	if p, ok := f.Extra["target_path"].(string); ok && p != "" {
		f.HasTarget = true
		if call.CWD != "" {
			if rel, err := filepath.Rel(call.CWD, p); err == nil {
				f.Extra["path_relative_to_working_directory"] = rel
			}
		}
		if sensitiveConfigPaths.MatchString(p) {
			f.SensitiveConfig = true
			f.Extra["file_governs_ci_or_deployment"] = true
		}
	}
	return f
}

// bashCommand returns the command string when the call is a shell invocation.
func bashCommand(call Call) (string, bool) {
	if !strings.EqualFold(call.Tool, "Bash") && !strings.EqualFold(call.Tool, "bash") {
		return "", false
	}
	var in map[string]any
	if json.Unmarshal(call.Input, &in) != nil {
		return "", false
	}
	cmd, ok := in["command"].(string)
	return cmd, ok
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func orElse(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// --- audit ------------------------------------------------------------------

// audit appends one JSON line per judgment. This is what makes the gate
// measurable: run real sessions, then score the log for false positives.
func audit(cfg Config, call Call, facts Facts, v *Verdict) {
	if cfg.AuditLog == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(cfg.AuditLog), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(cfg.AuditLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	rec := map[string]any{
		"at":           time.Now().Format(time.RFC3339),
		"harness":      call.Harness,
		"session":      call.SessionID,
		"agent_model":  call.Model,
		"tool":         call.Tool,
		"action":       facts.Summary,
		"user_request": truncate(call.UserRequest, 400),
		"decision":     v.Decision,
		"reason":       v.Reason,
		"signals":      v.Signals,
		"trace":        v.Trace,
		"latency_ms":   v.Latency.Milliseconds(),
		"input_tokens": v.InputTokens,
	}
	_ = json.NewEncoder(f).Encode(rec)
}
