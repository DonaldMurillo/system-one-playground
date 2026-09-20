package sos

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// CommandSelection contains the validated invocation and its help text.
type CommandSelection struct {
	Path   []string
	Values map[string]any
	Help   bool
	Usage  string
}

func buildCommands(p *Program) []Diagnostic {
	var ds []Diagnostic
	add := func(line int, msg string) { ds = append(ds, Diagnostic{line, 1, msg}) }
	var build func(*Statement, map[string]bool) *Command
	build = func(s *Statement, inherited map[string]bool) *Command {
		c := &Command{Name: match("command", s.Text)[1], Line: s.Line}
		seen := copyNames(inherited)
		children := map[string]bool{}
		described := false
		for _, st := range s.Body {
			switch st.Kind {
			case "parameter":
				m := match("parameter", st.Text)
				typ := m[3]
				if typ == "" {
					typ = "text"
				}
				if m[1] == "switch" {
					if m[3] != "" && m[3] != "boolean" {
						add(st.Line, "switch "+m[2]+" must have boolean type")
					}
					typ = "boolean"
				}
				a := Parameter{Name: m[2], Kind: m[1], Type: typ, Default: m[5]}
				if a.Kind == "argument" && a.Default != "" {
					add(st.Line, "argument "+a.Name+" is required and cannot declare a default")
				}
				if a.Name == "help" {
					add(st.Line, "help is reserved for command help")
				}
				if seen[a.Name] {
					add(st.Line, "duplicate or shadowed parameter: "+a.Name)
				}
				seen[a.Name] = true
				if m[4] != "" {
					if typ != "text" || a.Kind != "option" {
						add(st.Line, "choices are supported only for text options")
					}
					xs, e := splitExpressions(m[4])
					if e != nil {
						add(st.Line, "invalid choices")
					}
					unique := map[string]bool{}
					for _, x := range xs {
						v, e := strconv.Unquote(strings.TrimSpace(x))
						if e != nil {
							add(st.Line, "choices must be quoted text literals")
							continue
						}
						if unique[v] {
							add(st.Line, "duplicate choice: "+v)
						}
						unique[v] = true
						a.Choices = append(a.Choices, v)
					}
				}
				if a.Default != "" {
					if _, e := parameterValue(a, a.Default, true); e != nil {
						add(st.Line, e.Error())
					}
				}
				c.Parameters = append(c.Parameters, a)
			case "describe":
				if described {
					add(st.Line, "describe may appear only once per command")
				}
				described = true
				v, e := strconv.Unquote(match("describe", st.Text)[1])
				if e != nil {
					add(st.Line, "describe requires quoted text")
				}
				c.Description = v
			case "command":
				// Build after collecting every parent input, independent of declaration order.
			default:
				c.Statements = append(c.Statements, st)
			}
		}
		for _, st := range s.Body {
			if st.Kind == "command" {
				child := build(st, seen)
				if children[child.Name] {
					add(st.Line, "duplicate command: "+child.Name)
				}
				children[child.Name] = true
				c.Commands = append(c.Commands, child)
			}
		}
		if len(c.Commands) > 0 {
			if len(c.Statements) > 0 {
				add(c.Statements[0].Line, "command group cannot contain executable statements")
			}
			for _, a := range c.Parameters {
				if a.Kind == "argument" {
					add(c.Line, "command groups cannot declare positional argument "+a.Name)
				}
			}
		}
		return c
	}
	for _, s := range p.Statements {
		if s.Kind == "command" {
			if p.RootCommand != nil {
				add(s.Line, "declare exactly one root command")
				continue
			}
			p.RootCommand = build(s, map[string]bool{})
			p.Command = p.RootCommand.Name
			p.Parameters = p.RootCommand.Parameters
		}
	}
	if p.RootCommand != nil {
		for _, s := range p.Statements {
			if s.Kind != "command" && s.Kind != "to" && s.Kind != "schema" && s.Kind != "import" && !(s.Kind == "invalid" && strings.HasPrefix(s.Text, "criterion ")) {
				add(s.Line, "move executable statements inside a command; top level permits only declarations")
			}
		}
	}
	return ds
}

func commandAt(root *Command, path []string) (*Command, []Parameter, error) {
	c := root
	if c == nil {
		if len(path) > 0 {
			return nil, nil, fmt.Errorf("plain scripts have no subcommands")
		}
		return nil, nil, nil
	}
	params := append([]Parameter{}, c.Parameters...)
	for _, name := range path {
		var next *Command
		for _, child := range c.Commands {
			if child.Name == name {
				next = child
				break
			}
		}
		if next == nil {
			return nil, nil, fmt.Errorf("unknown command %q", name)
		}
		c = next
		params = append(params, c.Parameters...)
	}
	return c, params, nil
}

// CommandUsage renders help for a command and its inherited inputs.
func CommandUsage(root *Command, path []string) string {
	if root == nil {
		return "usage: script\n"
	}
	c, params, e := commandAt(root, path)
	if e != nil {
		return e.Error() + "\n"
	}
	name := strings.Join(append([]string{root.Name}, path...), " ")
	var b strings.Builder
	fmt.Fprintf(&b, "usage: %s", name)
	if len(c.Commands) > 0 {
		b.WriteString(" <command>")
	}
	for _, a := range params {
		if a.Kind == "argument" {
			fmt.Fprintf(&b, " %s", strings.ToUpper(a.Name))
		}
	}
	b.WriteString(" [options]\n")
	if c.Description != "" {
		fmt.Fprintf(&b, "\n%s\n", c.Description)
	}
	if len(c.Commands) > 0 {
		b.WriteString("\nCommands:\n")
		for _, child := range c.Commands {
			fmt.Fprintf(&b, "  %s  %s\n", child.Name, child.Description)
		}
	}
	b.WriteString("\nInputs:\n")
	for _, a := range params {
		prefix := "--"
		if a.Kind == "argument" {
			prefix = ""
		}
		fmt.Fprintf(&b, "  %s%s (%s)", prefix, a.Name, a.Type)
		if len(a.Choices) > 0 {
			fmt.Fprintf(&b, " choices: %s", strings.Join(a.Choices, ", "))
		}
		if a.Default != "" {
			fmt.Fprintf(&b, " default: %s", a.Default)
		} else if a.Kind != "switch" {
			b.WriteString(" required")
		}
		b.WriteByte('\n')
	}
	b.WriteString("  -h, --help\n")
	return b.String()
}

// SelectCommand validates CLI arguments without executing or resolving source.
func SelectCommand(p *Program, args []string) (sel *CommandSelection, err error) {
	sel = &CommandSelection{Values: map[string]any{}}
	if p == nil {
		return sel, fmt.Errorf("missing program")
	}
	defer func() { sel.Usage = CommandUsage(p.RootCommand, sel.Path) }()
	c := p.RootCommand
	seen := map[string]bool{}
	positionals := []string{}
	end := false
	var missingValue error
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !end && (arg == "--help" || arg == "-h") {
			sel.Help = true
			continue
		}
		if !end && arg == "--" {
			end = true
			continue
		}
		if c == nil {
			return sel, fmt.Errorf("plain scripts do not declare command inputs")
		}
		if !end && strings.HasPrefix(arg, "-") {
			if !strings.HasPrefix(arg, "--") {
				return sel, fmt.Errorf("unknown option %q", arg)
			}
			name, value, has := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
			_, params, _ := commandAt(p.RootCommand, sel.Path)
			var found *Parameter
			for j := range params {
				if params[j].Name == name && params[j].Kind != "argument" {
					found = &params[j]
					break
				}
			}
			if found == nil {
				return sel, fmt.Errorf("unknown option --%s for this command (child options follow the child name)", name)
			}
			if seen[name] {
				return sel, fmt.Errorf("duplicate option --%s", name)
			}
			seen[name] = true
			if found.Kind == "switch" && !has {
				value = "true"
			} else if !has {
				if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") || args[i+1] == "-h" {
					if missingValue == nil {
						missingValue = fmt.Errorf("missing value for --%s", name)
					}
					continue
				}
				i++
				value = args[i]
			}
			sel.Values[name] = value
			continue
		}
		if len(c.Commands) > 0 {
			if end {
				return sel, fmt.Errorf("expected subcommand before --")
			}
			var child *Command
			for _, x := range c.Commands {
				if x.Name == arg {
					child = x
					break
				}
			}
			if child == nil {
				return sel, fmt.Errorf("unknown command %q", arg)
			}
			c = child
			sel.Path = append(sel.Path, arg)
			continue
		}
		positionals = append(positionals, arg)
	}
	if c == nil {
		return sel, nil
	}
	if len(c.Commands) > 0 {
		if missingValue != nil && !sel.Help {
			return sel, missingValue
		}
		sel.Help = true
		return sel, nil
	}
	_, params, _ := commandAt(p.RootCommand, sel.Path)
	n := 0
	for _, a := range params {
		if a.Kind == "argument" && n < len(positionals) {
			sel.Values[a.Name] = positionals[n]
			n++
		}
	}
	if n < len(positionals) {
		return sel, fmt.Errorf("unexpected argument %q", positionals[n])
	}
	if sel.Help {
		return sel, nil
	}
	if missingValue != nil {
		return sel, missingValue
	}
	sel.Values, err = validateCommandValues(params, sel.Values)
	return sel, err
}

func validateCommandValues(params []Parameter, input map[string]any) (map[string]any, error) {
	out := map[string]any{}
	known := map[string]bool{}
	for _, a := range params {
		known[a.Name] = true
		v, ok := input[a.Name]
		def := false
		if !ok {
			if a.Default != "" {
				v = a.Default
				def = true
			} else if a.Kind == "switch" {
				v = false
			} else {
				return nil, fmt.Errorf("missing required %s %q", a.Kind, a.Name)
			}
		}
		v, e := parameterValue(a, v, def)
		if e != nil {
			return nil, e
		}
		out[a.Name] = v
	}
	for k := range input {
		if !known[k] {
			return nil, fmt.Errorf("unknown input %q", k)
		}
	}
	return out, nil
}
func parameterValue(a Parameter, v any, def bool) (any, error) {
	fail := func() (any, error) { return nil, fmt.Errorf("%s: invalid %s value", a.Name, a.Type) }
	if s, ok := v.(string); ok {
		if def && strings.HasPrefix(s, "\"") {
			x, e := strconv.Unquote(s)
			if e != nil {
				return fail()
			}
			s = x
		}
		switch a.Type {
		case "text", "file", "folder":
			v = s
		case "number", "integer":
			n, e := strconv.ParseFloat(s, 64)
			if e != nil {
				return fail()
			}
			v = n
		case "boolean":
			if s == "on" {
				s = "true"
			}
			if s == "off" {
				s = "false"
			}
			b, e := strconv.ParseBool(s)
			if e != nil {
				return fail()
			}
			v = b
		case "duration":
			d, e := time.ParseDuration(s)
			if e != nil {
				return fail()
			}
			v = d
		}
	}
	switch a.Type {
	case "text", "file", "folder":
		if _, ok := v.(string); !ok {
			return fail()
		}
	case "number", "integer":
		var n float64
		switch x := v.(type) {
		case float64:
			n = x
		case int:
			n = float64(x)
		default:
			return fail()
		}
		if math.IsNaN(n) || math.IsInf(n, 0) || a.Type == "integer" && n != math.Trunc(n) {
			return fail()
		}
		v = n
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fail()
		}
	case "duration":
		if _, ok := v.(time.Duration); !ok {
			return fail()
		}
	}
	if len(a.Choices) > 0 {
		good := false
		for _, choice := range a.Choices {
			if v == choice {
				good = true
			}
		}
		if !good {
			return nil, fmt.Errorf("%s must be one of: %s", a.Name, strings.Join(a.Choices, ", "))
		}
	}
	return v, nil
}

// SelectedSource preserves line positions while excluding unselected command bodies.
// It never resolves text or performs provider calls.
func SelectedSource(p *Program, path []string) (string, error) {
	if p == nil {
		return "", fmt.Errorf("missing program")
	}
	c, _, e := commandAt(p.RootCommand, path)
	if e != nil {
		return "", e
	}
	if c == nil {
		return p.Source, nil
	}
	if len(c.Commands) > 0 {
		return "", fmt.Errorf("select a leaf command")
	}
	lines := strings.Split(p.Source, "\n")
	scan := scanSemantic(p.Source)
	var blank func(*semNode)
	blank = func(n *semNode) {
		lines[n.line.num-1] = ""
		for _, child := range n.children {
			blank(child)
		}
	}
	var walk func([]*semNode, int)
	walk = func(nodes []*semNode, depth int) {
		for _, n := range nodes {
			if n.form != "command" {
				continue
			}
			if depth >= 0 && (depth >= len(path) || n.m[1] != path[depth]) {
				blank(n)
				continue
			}
			walk(n.children, depth+1)
		}
	}
	walk(scan.nodes, -1)
	// Only statically reachable shared actions can incur interpretation work.
	actions := map[string]*semNode{}
	for _, n := range scan.nodes {
		if n.form == "to" {
			actions[n.m[1]] = n
		}
	}
	reachable := map[string]bool{}
	var visitCalls func([]*semNode)
	visitCalls = func(nodes []*semNode) {
		for _, n := range nodes {
			if lines[n.line.num-1] == "" {
				continue
			}
			called := ""
			if n.form == "call" {
				called = n.m[1]
			}
			if n.form == "handler" && len(n.m) > 2 {
				if m := match("call", n.m[2]); m != nil {
					called = m[1]
				}
			}
			if called != "" && !reachable[called] {
				reachable[called] = true
				if action := actions[called]; action != nil {
					visitCalls(action.children)
				}
			}
			visitCalls(n.children)
		}
	}
	for _, n := range scan.nodes {
		if n.form == "command" {
			visitCalls([]*semNode{n})
		}
	}
	for name, n := range actions {
		if !reachable[name] {
			blank(n)
		}
	}
	return strings.Join(lines, "\n"), nil
}

// ValidateCommandInputs checks a program invocation without provider or filesystem access.
func ValidateCommandInputs(p *Program, path []string, args map[string]any) (map[string]any, error) {
	if p == nil {
		return nil, fmt.Errorf("missing program")
	}
	c, params, e := commandAt(p.RootCommand, path)
	if e != nil {
		return nil, e
	}
	if c == nil {
		if len(args) > 0 {
			return nil, fmt.Errorf("plain scripts do not declare command inputs")
		}
		return map[string]any{}, nil
	}
	if len(c.Commands) > 0 {
		return nil, fmt.Errorf("select a leaf command")
	}
	return validateCommandValues(params, args)
}
