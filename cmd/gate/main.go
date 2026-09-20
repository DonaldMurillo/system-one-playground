// Command gate is a pre-flight judgment gate for agent tool calls.
//
// It sits in front of a coding agent's side-effecting tools, asks TypeSafe a
// handful of narrow questions about the proposed call, and returns allow, ask
// or deny. Code owns the policy; the model only supplies the judgments.
//
// Three protocol modes, one decision core:
//
//	gate -mode=claude   # Claude Code PreToolUse hook: reads hook JSON on stdin
//	gate -mode=omp      # omp tool_call hook: reads normalized JSON on stdin
//	gate -mode=check    # human-readable one-shot, for testing a call by hand
//
// It always fails open. A missing API key, a timeout, a malformed payload or a
// panic yields "allow", because a gate that breaks the agent loop is worse than
// no gate at all.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

func main() {
	mode := flag.String("mode", "check", "protocol: claude, omp, check, or eval")
	tool := flag.String("tool", "", "check mode: tool name, e.g. Bash")
	input := flag.String("input", "", "check mode: tool input as JSON, or a bare command string for Bash")
	request := flag.String("request", "", "check mode: the user's original request")
	timeout := flag.Duration("timeout", 4*time.Second, "overall deadline; on expiry the call is allowed")
	verbose := flag.Bool("v", false, "print every judgment and the policy trace")
	corpus := flag.String("corpus", "", "eval mode: path to a labelled corpus JSON (default: built-in)")
	workers := flag.Int("workers", 8, "eval mode: concurrent judgments")
	evalModel := flag.String("model", "", "eval mode: TypeSafe model override")
	payload := flag.String("payload", "", "omp mode: the request JSON, for callers that cannot write to stdin")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	switch *mode {
	case "claude":
		runClaude(ctx)
	case "omp":
		runOMP(ctx, *payload)
	case "check":
		runCheck(ctx, *tool, *input, *request, *verbose)
	case "eval":
		runEval(context.Background(), *corpus, *workers, *verbose, *evalModel)
	default:
		fmt.Fprintf(os.Stderr, "gate: unknown mode %q\n", *mode)
		os.Exit(2)
	}
}

// --- Claude Code PreToolUse -------------------------------------------------

// claudeInput is the subset of the PreToolUse payload the gate reads.
// See https://code.claude.com/docs/en/hooks#pretooluse-input
type claudeInput struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	CWD            string          `json:"cwd"`
	PermissionMode string          `json:"permission_mode"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolUseID      string          `json:"tool_use_id"`
}

type claudeOutput struct {
	HookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision,omitempty"`
		PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
		AdditionalContext        string `json:"additionalContext,omitempty"`
	} `json:"hookSpecificOutput"`
}

func runClaude(ctx context.Context) {
	// Emit a decision no matter what happens, including on panic.
	out := claudeOutput{}
	out.HookSpecificOutput.HookEventName = "PreToolUse"
	emit := func(v *Verdict) {
		if v != nil && v.Decision != DecisionAllow {
			out.HookSpecificOutput.PermissionDecision = string(v.Decision)
			out.HookSpecificOutput.PermissionDecisionReason = v.Reason
		}
		// An "allow" is left empty: the gate defers to the user's normal
		// permission rules rather than auto-approving on their behalf.
		_ = json.NewEncoder(os.Stdout).Encode(out)
	}
	defer func() {
		if r := recover(); r != nil {
			logf("panic: %v", r)
			emit(nil)
		}
	}()

	var in claudeInput
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		logf("decode stdin: %v", err)
		emit(nil)
		return
	}

	call := Call{
		Tool:      in.ToolName,
		Input:     in.ToolInput,
		CWD:       in.CWD,
		SessionID: in.SessionID,
		Harness:   "claude-code",
	}
	call.UserRequest = lastTypedPrompt(in.TranscriptPath)

	v := Judge(ctx, call)
	emit(v)
}

// --- omp tool_call ----------------------------------------------------------

// ompInput is what the omp hook adapter sends us. It mirrors omp's ToolCallEvent
// plus the context the hook can see.
type ompInput struct {
	ToolName    string          `json:"toolName"`
	ToolCallID  string          `json:"toolCallId"`
	Input       json.RawMessage `json:"input"`
	CWD         string          `json:"cwd"`
	UserRequest string          `json:"userRequest"`
	SessionID   string          `json:"sessionId"`
	Model       string          `json:"model"`
}

// ompOutput matches omp's ToolCallEventResult.
type ompOutput struct {
	Block  bool   `json:"block,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Ask has no omp equivalent: omp either blocks or lets its own approval
	// prompt handle it. We surface "ask" as a non-blocking note the hook prints.
	Note string `json:"note,omitempty"`
}

func runOMP(ctx context.Context, payload string) {
	out := ompOutput{}
	emit := func() { _ = json.NewEncoder(os.Stdout).Encode(out) }
	defer func() {
		if r := recover(); r != nil {
			logf("panic: %v", r)
			out = ompOutput{}
			emit()
		}
	}()

	var in ompInput
	// omp's pi.exec() cannot write to a child's stdin, so the hook passes the
	// payload as an argument instead.
	var derr error
	if payload != "" {
		derr = json.Unmarshal([]byte(payload), &in)
	} else {
		derr = json.NewDecoder(os.Stdin).Decode(&in)
	}
	if derr != nil {
		logf("decode payload: %v", derr)
		emit()
		return
	}
	v := Judge(ctx, Call{
		Tool:        in.ToolName,
		Input:       in.Input,
		CWD:         in.CWD,
		UserRequest: in.UserRequest,
		SessionID:   in.SessionID,
		Model:       in.Model,
		Harness:     "omp",
	})
	switch {
	case v == nil:
	case v.Decision == DecisionDeny:
		out.Block, out.Reason = true, v.Reason
	case v.Decision == DecisionAsk:
		out.Note = v.Reason
	}
	emit()
}

// --- check ------------------------------------------------------------------

func runCheck(ctx context.Context, tool, input, request string, verbose bool) {
	if tool == "" {
		fmt.Fprintln(os.Stderr, "gate: -tool is required in check mode")
		os.Exit(2)
	}
	raw := json.RawMessage(strings.TrimSpace(input))
	if !json.Valid(raw) {
		// Convenience: a bare string is treated as a Bash command or a file path.
		key := "command"
		if tool != "Bash" {
			key = "file_path"
		}
		b, _ := json.Marshal(map[string]string{key: input})
		raw = b
	}
	cwd, _ := os.Getwd()
	v := Judge(ctx, Call{Tool: tool, Input: raw, CWD: cwd, UserRequest: request, Harness: "check"})
	if v == nil {
		fmt.Println("allow  (gate inactive: no judgment was made)")
		return
	}
	fmt.Printf("%-5s  %s\n", v.Decision, v.Reason)
	if verbose {
		if v.Skipped != "" {
			fmt.Printf("  skipped: %s\n", v.Skipped)
			return
		}
		fmt.Printf("  latency %s, %d tokens, model %s\n", v.Latency.Round(time.Millisecond), v.InputTokens, v.Model)
		for _, s := range v.Signals {
			fmt.Printf("  %-18s %.2f  %s\n", s.Name, s.Value, s.Detail)
		}
		for _, t := range v.Trace {
			fmt.Printf("  rule: %s\n", t)
		}
	}
}

// --- shared helpers ---------------------------------------------------------

// logf writes to stderr. Claude Code shows hook stderr only in debug mode, so
// this is safe for diagnostics and never reaches the model.
func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "typesafe-gate: "+format+"\n", args...)
}

// lastTypedPrompt returns the most recent prompt the human actually typed.
// Tool results share the "user" type in the transcript, so we key off
// promptSource, which only real input carries.
func lastTypedPrompt(path string) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		logf("open transcript: %v", err)
		return ""
	}
	defer f.Close()

	// Transcripts grow large; read only the tail.
	const tail = 512 << 10
	if st, err := f.Stat(); err == nil && st.Size() > tail {
		if _, err := f.Seek(-tail, io.SeekEnd); err != nil {
			return ""
		}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var row struct {
			Type         string `json:"type"`
			PromptSource string `json:"promptSource"`
			Message      struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(lines[i]), &row) != nil {
			continue
		}
		if row.Type != "user" || row.PromptSource == "" {
			continue
		}
		if s := contentText(row.Message.Content); s != "" {
			return truncate(s, 2000)
		}
	}
	return ""
}

// contentText flattens an Anthropic message content field, which is either a
// bare string or an array of typed blocks.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

var errInactive = errors.New("gate inactive")
