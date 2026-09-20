// Package sossyntax provides SOS's own tolerant editor syntax engine. It keeps
// comments, incomplete strings and source spans that the execution parser does
// not need. Positions use UTF-16 columns, as required by LSP and Monaco.
package sossyntax

import (
	"strings"
	"unicode"
	"unicode/utf16"
)

// Token is a lossless lexical span (zero-based UTF-16 columns).
type Token struct {
	Kind, Text string
	Start, End int
}

// Line retains a source line and its tokens for incremental reuse.
type Line struct {
	Text   string
	Indent int
	Tokens []Token
}

// Block is a colon-led indented source region, including its header.
type Block struct{ Start, End int }

// Document is an immutable editor snapshot. Update reuses unchanged tokenized lines.
type Document struct {
	Lines  []Line
	Blocks []Block
}

func Parse(source string) *Document { return Update(nil, source) }

// Update handles insertions/deletions by retaining the unchanged prefix/suffix.
// Only changed lines are lexed; block spans are rebuilt without re-lexing text.
func Update(previous *Document, source string) *Document {
	texts := strings.Split(strings.ReplaceAll(source, "\r\n", "\n"), "\n")
	d := &Document{Lines: make([]Line, len(texts))}
	prefix, suffix := 0, 0
	if previous != nil {
		for prefix < len(texts) && prefix < len(previous.Lines) && texts[prefix] == previous.Lines[prefix].Text {
			d.Lines[prefix] = previous.Lines[prefix]
			prefix++
		}
		for suffix < len(texts)-prefix && suffix < len(previous.Lines)-prefix && texts[len(texts)-1-suffix] == previous.Lines[len(previous.Lines)-1-suffix].Text {
			d.Lines[len(texts)-1-suffix] = previous.Lines[len(previous.Lines)-1-suffix]
			suffix++
		}
	}
	for i := prefix; i < len(texts)-suffix; i++ {
		d.Lines[i] = lexLine(texts[i])
	}
	stack := []int{}
	for i, line := range d.Lines {
		tokens := line.Tokens
		if len(tokens) > 0 && tokens[len(tokens)-1].Kind == "comment" {
			tokens = tokens[:len(tokens)-1]
		}
		if len(tokens) == 0 {
			continue
		}
		for len(stack) > 0 && line.Indent <= d.Lines[stack[len(stack)-1]].Indent {
			start := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if i-1 > start {
				d.Blocks = append(d.Blocks, Block{start, i - 1})
			}
		}
		if tokens[len(tokens)-1].Text == ":" {
			stack = append(stack, i)
		}
	}
	for _, start := range stack {
		if len(d.Lines)-1 > start {
			d.Blocks = append(d.Blocks, Block{start, len(d.Lines) - 1})
		}
	}
	return d
}

func lexLine(text string) Line {
	line := Line{Text: text}
	runes := []rune(text)
	col := 0
	for i := 0; i < len(runes); {
		c := runes[i]
		if unicode.IsSpace(c) {
			if len(line.Tokens) == 0 {
				if c == '\t' {
					line.Indent += 2
				} else {
					line.Indent++
				}
			}
			col += width(c)
			i++
			continue
		}
		start, begin := col, i
		kind := "punctuation"
		if c == '#' {
			kind = "comment"
			i = len(runes)
		} else if c == '"' {
			kind = "string"
			i++
			closed := false
			for i < len(runes) {
				if runes[i] == '\\' {
					i++
					if i < len(runes) {
						i++
					}
					continue
				}
				if runes[i] == '"' {
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				kind = "incompleteString"
			}
		} else if unicode.IsLetter(c) || c == '_' {
			kind = "identifier"
			i++
			for i < len(runes) && (unicode.IsLetter(runes[i]) || unicode.IsDigit(runes[i]) || runes[i] == '_' || runes[i] == '-') {
				i++
			}
		} else if unicode.IsDigit(c) {
			kind = "number"
			i++
			for i < len(runes) && (unicode.IsDigit(runes[i]) || runes[i] == '.') {
				i++
			}
		} else {
			i++
		}
		for _, r := range runes[begin:i] {
			col += width(r)
		}
		line.Tokens = append(line.Tokens, Token{kind, string(runes[begin:i]), start, col})
	}
	return line
}
func width(r rune) int {
	if utf16.RuneLen(r) == 2 {
		return 2
	}
	return 1
}
