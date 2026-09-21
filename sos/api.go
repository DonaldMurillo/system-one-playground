// Package sos implements the canonical SysOneScript sentence language.
package sos

import (
	"context"
	"github.com/DonaldMurillo/system-one-playground/sosconfig"
	"io"
)

var Version = "0.6.0"

// ReleaseMarker is linked alongside Version so packaged cross-platform
// runtimes can be verified without executing a foreign binary.
var ReleaseMarker = "SysOneScriptVersion=development"

type Diagnostic struct {
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Message string `json:"message"`
}
type Statement struct {
	Kind string       `json:"kind"`
	Text string       `json:"text"`
	Line int          `json:"line"`
	Body []*Statement `json:"body,omitempty"`
}
type Command struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Parameters  []Parameter  `json:"parameters,omitempty"`
	Commands    []*Command   `json:"commands,omitempty"`
	Statements  []*Statement `json:"statements,omitempty"`
	Line        int          `json:"line"`
}
type Program struct {
	RootCommand *Command               `json:"rootCommand,omitempty"`
	Source      string                 `json:"source"`
	Statements  []*Statement           `json:"statements"`
	Command     string                 `json:"command,omitempty"`
	Parameters  []Parameter            `json:"parameters,omitempty"`
	Definitions map[string]*RecordDef  `json:"definitions,omitempty"`
	Failures    map[string]*FailureDef `json:"failures,omitempty"`
	// Modules is the resolved import graph; nil for plain Parse results.
	Modules *ModuleTable `json:"-"`
}
type Parameter struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Kind    string   `json:"kind"`
	Default string   `json:"default,omitempty"`
	Choices []string `json:"choices,omitempty"`
}
type Trace struct {
	Item         any    `json:"item,omitempty"`
	Decision     string `json:"decision,omitempty"`
	Reason       string `json:"reason,omitempty"`
	Line         int    `json:"line"`
	Question     string `json:"question"`
	Model        string `json:"model"`
	Answer       any    `json:"answer"`
	Milliseconds int64  `json:"milliseconds"`
	InputTokens  int    `json:"inputTokens"`
	Replay       bool   `json:"replay"`
}

// DebugFrame identifies one executable source location. Path is absolute when
// the runner was given a source path, and Line is one-based.
type DebugFrame struct {
	Name   string `json:"name,omitempty"`
	Path   string `json:"path,omitempty"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
	Kind   string `json:"kind,omitempty"`
	Text   string `json:"text,omitempty"`
	Depth  int    `json:"depth"`
	// VariableTypes identifies statically known named-record shapes for the
	// debugger without adding metadata to the underlying JSON-shaped values.
	VariableTypes map[string]string `json:"variableTypes,omitempty"`
}

// Debugger is the runtime boundary used by the native debug adapter. The
// runtime calls BeforeStatement immediately before executing each statement.
// Implementations may block there to implement breakpoints and stepping. The
// variables map is a JSON-shaped snapshot and can safely be retained.
type Debugger interface {
	BeforeStatement(context.Context, DebugFrame, []DebugFrame, map[string]any) error
}
type Options struct {
	CommandPath []string
	Dir         string
	Args        map[string]any
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
	MaxSteps    int
	MaxCalls    int
	Model       string
	Record      string
	Replay      string
	// Config supplies a resolved policy (for packaged programs). Nil discovers host config.
	Config *sosconfig.Effective
	// Budget optionally shares request accounting across consumers.
	Budget *RequestBudget
	// Resolution reuses an inspectable saved interpretation. Locked forbids new resolution calls.
	Resolution *Analysis
	Locked     bool
	OnTrace    func(Trace)
	// SourcePath gives runtime diagnostics and debugger stops a stable source
	// identity. It is optional for embedders that execute in-memory programs.
	SourcePath string
	// Debugger pauses before executable statements when a debug adapter is
	// attached. It is nil for ordinary runs.
	Debugger Debugger
}
type Result struct {
	// Usage reports live provider admissions; replay adds no new usage.
	Analysis  *Analysis      `json:"analysis,omitempty"`
	Usage     BudgetSnapshot `json:"usage"`
	Variables map[string]any `json:"variables"`
	Traces    []Trace        `json:"traces"`
	Steps     int            `json:"steps"`
	Failure   map[string]any `json:"failure,omitempty"`
}

// StopError is explicit application termination requested by `stop`. It is
// fatal to normal failure handlers but retains the user-facing message at the
// CLI and standalone-runner boundaries.
type StopError struct{ Message string }

func (e *StopError) Error() string { return e.Message }
func (e *StopError) ExitCode() int { return 1 }

// EvaluateDebugExpression evaluates a read-only language expression against a
// debugger variable snapshot. It intentionally exposes the same expression
// grammar as scripts without exposing runtime effects or provider calls.
func EvaluateDebugExpression(expression string, variables map[string]any) (any, error) {
	return evaluate(expression, variables, nil)
}

// Public API implemented in the language core:
// Parse(source string) (*Program, []Diagnostic)
// LoadProgram(filename, source string) (*Program, []Diagnostic) -- module-aware
// LoadProgramFromGraph(filename, source string, g *ModuleGraph) (*Program, []Diagnostic)
// ParseWithVocabulary(source string, modules *ModuleTable) (*Program, []Diagnostic)
// Vocabulary(filename, source string) (*VocabularyCatalog, []Diagnostic) -- offline catalog
// Check(source string) []Diagnostic
// CheckFile(filename, source string) []Diagnostic -- module-aware
// Format(source string) (string, []Diagnostic)
// Run(ctx context.Context, program *Program, options Options) (*Result, error)
// LoadEnv(path string) error -- preserves existing environment, never logs values.
// Keywords() []string
