package studio

import (
	"github.com/DonaldMurillo/system-one-playground/sos"
)

// Command declaration metadata for the editor: the picker and input panel are
// built from what the core parsed, never re-derived here. Plain scripts have
// no command tree and get a nil metadata tree.

// commandMeta is one node of the declaration tree sent to the browser.
type commandMeta struct {
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Inputs      []inputMeta   `json:"inputs,omitempty"`
	Commands    []commandMeta `json:"commands,omitempty"`
}

// inputMeta is one declared input of a command scope. Required is derived the
// way the core treats declarations: switches default off, options without a
// default must be supplied, arguments are positional and always required.
type inputMeta struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Type     string   `json:"type"`
	Required bool     `json:"required,omitempty"`
	Default  string   `json:"default,omitempty"`
	Choices  []string `json:"choices,omitempty"`
}

// commandsMeta serializes the parsed root command, or nil for a plain script.
func commandsMeta(program *sos.Program) *commandMeta {
	if program == nil || program.RootCommand == nil {
		return nil
	}
	root := commandNodeMeta(program.RootCommand)
	if root == nil {
		return nil
	}
	return root
}

func commandNodeMeta(cmd *sos.Command) *commandMeta {
	if cmd == nil {
		return nil
	}
	node := commandMeta{
		Name:        cmd.Name,
		Description: cmd.Description,
	}
	for i := range cmd.Parameters {
		node.Inputs = append(node.Inputs, inputNodeMeta(&cmd.Parameters[i]))
	}
	for _, child := range cmd.Commands {
		if childMeta := commandNodeMeta(child); childMeta != nil {
			node.Commands = append(node.Commands, *childMeta)
		}
	}
	return &node
}

func inputNodeMeta(p *sos.Parameter) inputMeta {
	required := p.Kind == "argument"
	if p.Kind == "option" && p.Default == "" {
		required = true
	}
	in := inputMeta{
		Name:     p.Name,
		Kind:     p.Kind,
		Type:     p.Type,
		Required: required,
		Default:  p.Default,
	}
	if len(p.Choices) > 0 {
		in.Choices = append([]string(nil), p.Choices...)
	}
	return in
}
