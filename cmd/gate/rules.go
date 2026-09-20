package main

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Deterministic rules run before the model. Anything code can recognize
// exactly belongs in code: it costs nothing, cannot be talked out of a verdict
// by the text it is judging, and still works when the API is unreachable.
//
// Keep this list short and unambiguous. A pattern that needs judgment about
// intent is the model's job, not a regex's.

type hardRule struct {
	name     string
	pattern  *regexp.Regexp
	decision Decision
	reason   string
}

var hardRules = []hardRule{
	{
		name:     "rm-root-or-home",
		pattern:  regexp.MustCompile(`\brm\s+(-[a-zA-Z]*\s+)*-?[a-zA-Z]*[rR][a-zA-Z]*[fF]?[a-zA-Z]*\s+(/|~|\$HOME|/\*|~/\*)\s*$`),
		decision: DecisionDeny,
		reason:   "Blocked: this recursively deletes the filesystem root or your home directory.",
	},
	{
		name:     "disk-overwrite",
		pattern:  regexp.MustCompile(`\b(dd|mkfs\S*)\b.*\bof=/dev/\w|>\s*/dev/(sd|nvme|disk)`),
		decision: DecisionDeny,
		reason:   "Blocked: this writes directly to a block device.",
	},
	{
		name:     "curl-pipe-shell",
		pattern:  regexp.MustCompile(`\b(curl|wget)\b[^|]*\|\s*(sudo\s+)?(ba|z|k|d)?sh\b`),
		decision: DecisionDeny,
		reason:   "Blocked: this pipes a downloaded script straight into a shell. Download it, read it, then run it.",
	},
	{
		name:     "git-discard-local-edits",
		pattern:  regexp.MustCompile(`\bgit\s+(checkout|restore)\s+(--\s+\.|\.$|--staged\s+\.|-f\b)`),
		decision: DecisionAsk,
		reason:   "This throws away uncommitted edits in the working tree.",
	},
	{
		name:     "git-hard-reset-or-clean",
		pattern:  regexp.MustCompile(`\bgit\s+(reset\s+--hard|clean\s+(-[a-zA-Z]*\s*)*-[a-zA-Z]*[fd])`),
		decision: DecisionAsk,
		reason:   "This discards uncommitted work permanently.",
	},
	{
		name:     "git-force-push",
		pattern:  regexp.MustCompile(`\bgit\s+push\b.*(--force\b|--force-with-lease\b|\s-f\b)`),
		decision: DecisionAsk,
		reason:   "This rewrites a branch other people may have pulled.",
	},
	{
		name:     "sql-drop",
		pattern:  regexp.MustCompile(`(?i)\b(drop\s+(database|table|schema)|truncate\s+table|delete\s+from\s+\w+\s*;)`),
		decision: DecisionAsk,
		reason:   "This destroys database contents.",
	},
	{
		name:     "chmod-777-or-recursive-root",
		pattern:  regexp.MustCompile(`\bchmod\s+(-R\s+)?777\b|\bchown\s+-R\b.*\s/(usr|etc|var|bin)\b`),
		decision: DecisionAsk,
		reason:   "This changes permissions or ownership in a way that is hard to unwind.",
	},
	{
		name:     "history-or-credential-dump",
		pattern:  regexp.MustCompile(`\b(cat|less|more|head|tail|printenv|env)\b[^|;&]*(\.env\b|\.aws/credentials|\.ssh/id_|\.netrc|_history\b)`),
		decision: DecisionAsk,
		reason:   "This reads a file that normally holds credentials.",
	},
}

// sensitiveConfigPaths govern CI, deployment, or infrastructure. Editing them
// while working on something else is the classic quiet scope creep.
var sensitiveConfigPaths = regexp.MustCompile(`(?:^|/)(\.github/workflows/|\.gitlab-ci\.yml|Dockerfile|docker-compose\.ya?ml|\.circleci/|Jenkinsfile|\.buildkite/|deploy\.(ya?ml|toml|json)|k8s/|kubernetes/|helm/|terraform/|\.tf$|serverless\.ya?ml|fly\.toml|vercel\.json|netlify\.toml|Procfile)`)

// protectedPaths are files whose modification reaches beyond the current task.
var protectedPaths = regexp.MustCompile(`(?:^|/)(\.ssh/|\.aws/|\.gnupg/|\.zshrc|\.zprofile|\.bashrc|\.bash_profile|\.profile|/etc/|\.git/config$|\.git/hooks/)`)

// checkHardRules returns a verdict when a deterministic rule matches, or nil to
// hand the call to the model.
func checkHardRules(call Call, facts Facts) *Verdict {
	subject := facts.Summary
	var in map[string]any
	_ = json.Unmarshal(call.Input, &in)
	if cmd, ok := in["command"].(string); ok {
		subject = cmd
	}

	for _, r := range hardRules {
		if r.pattern.MatchString(subject) {
			return &Verdict{
				Decision: r.decision,
				Reason:   r.reason,
				Trace:    []string{"hard rule: " + r.name},
				Skipped:  "matched deterministic rule, no model call",
			}
		}
	}

	// Writing to a protected path is an ask regardless of what it contains.
	if p, ok := in["file_path"].(string); ok && protectedPaths.MatchString(p) {
		if call.Tool == "Write" || call.Tool == "Edit" || strings.EqualFold(call.Tool, "write") || strings.EqualFold(call.Tool, "edit") {
			return &Verdict{
				Decision: DecisionAsk,
				Reason:   "This edits a file outside the project that affects your shell, keys, or git configuration.",
				Trace:    []string{"hard rule: protected-path"},
				Skipped:  "matched deterministic rule, no model call",
			}
		}
	}
	return nil
}

// readOnlyCommands are shell commands that cannot change anything on their own.
// Recognizing them in code saves a round trip on the majority of an agent's
// calls: exploring a codebase is mostly ls, grep, sed -n and git log.
var readOnlyCommands = map[string]bool{
	"ls": true, "cat": true, "head": true, "tail": true, "wc": true, "file": true,
	"find": true, "grep": true, "rg": true, "egrep": true, "fgrep": true,
	"sed": true, "awk": true, "sort": true, "uniq": true, "cut": true, "tr": true,
	"echo": true, "pwd": true, "date": true, "which": true, "type": true,
	"basename": true, "dirname": true, "realpath": true, "stat": true, "du": true, "df": true,
	"tree": true, "jq": true, "yq": true, "column": true, "nl": true, "diff": true,
}

// readOnlyGitSubcommands are the git verbs that only report.
var readOnlyGitSubcommands = map[string]bool{
	"status": true, "log": true, "diff": true, "show": true, "branch": true,
	"remote": true, "blame": true, "describe": true, "rev-parse": true,
	"ls-files": true, "shortlog": true, "tag": true, "config": true, "stash": true,
}

// writeIndicators are shell constructs that can produce a change, so any
// command containing one is handed to the model regardless of how it starts.
var writeIndicators = regexp.MustCompile(`>|<\(|\$\(|` + "`" + `|&&|\|\||;|\bxargs\b|\btee\b|\bsudo\b`)

// isReadOnlyCommand reports whether every segment of a pipeline is a known
// reporting command with no redirection. It is deliberately conservative:
// anything it cannot prove read-only falls through to the full judgment.
func isReadOnlyCommand(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" || writeIndicators.MatchString(cmd) {
		return false
	}
	for _, segment := range strings.Split(cmd, "|") {
		fields := strings.Fields(segment)
		if len(fields) == 0 {
			return false
		}
		name := fields[0]
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		switch {
		case name == "git":
			// git config only reports when it is not setting a value, which
			// takes more than one argument after the subcommand.
			if len(fields) < 2 || !readOnlyGitSubcommands[fields[1]] {
				return false
			}
			if fields[1] == "config" && len(fields) > 3 {
				return false
			}
			if fields[1] == "stash" && len(fields) > 2 && fields[2] != "list" && fields[2] != "show" {
				return false
			}
		case name == "sed":
			// sed -i edits in place; sed -n prints.
			for _, f := range fields[1:] {
				if strings.HasPrefix(f, "-i") {
					return false
				}
			}
		case !readOnlyCommands[name]:
			return false
		}
	}
	return true
}
