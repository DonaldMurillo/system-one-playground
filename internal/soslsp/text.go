package soslsp

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// LSP positions are zero based line and UTF-16 code unit character; the sos
// core reports one based lines and byte based columns. These helpers convert.

type lspPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type lspRange struct {
	Start lspPosition `json:"start"`
	End   lspPosition `json:"end"`
}

// byteToChar converts a byte offset within line to a UTF-16 character offset.
func byteToChar(line string, col int) int {
	if col < 0 {
		col = 0
	}
	if col > len(line) {
		col = len(line)
	}
	char := 0
	for i := 0; i < col; {
		r, size := utf8.DecodeRuneInString(line[i:])
		if size == 0 {
			break
		}
		char += utf16Width(r)
		i += size
	}
	return char
}

// charToByte converts a UTF-16 character offset within line to a byte offset,
// snapped forward past any surrogate pair and clamped to the line length.
func charToByte(line string, char int) int {
	if char <= 0 {
		return 0
	}
	cur := 0
	for i := 0; i < len(line); {
		if cur >= char {
			return i
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		if size == 0 {
			break
		}
		cur += utf16Width(r)
		i += size
	}
	return len(line)
}

func utf16Width(r rune) int {
	if r > 0xFFFF {
		return 2
	}
	return 1
}

// charWidthAt returns the UTF-16 width of the character at byte offset col.
func charWidthAt(line string, col int) int {
	if col < 0 || col >= len(line) {
		return 0
	}
	for col > 0 && !utf8.RuneStart(line[col]) {
		col--
	}
	r, size := utf8.DecodeRuneInString(line[col:])
	if size == 0 {
		return 0
	}
	return utf16Width(r)
}

// lineAt returns the zero based line without its line terminator.
func lineAt(source string, line int) string {
	lines := strings.Split(source, "\n")
	if line < 0 {
		line = 0
	}
	if line >= len(lines) {
		line = len(lines) - 1
	}
	return strings.TrimSuffix(lines[line], "\r")
}

// lineCount returns the number of lines, counting a trailing unterminated
// line and treating the empty string as one line.
func lineCount(source string) int {
	return strings.Count(source, "\n") + 1
}

// endPosition is the exclusive end position of the whole document.
func endPosition(source string) lspPosition {
	idx := strings.LastIndexByte(source, '\n')
	if idx < 0 {
		return lspPosition{Line: 0, Character: byteToChar(source, len(source))}
	}
	last := source[idx+1:]
	return lspPosition{Line: lineCount(source) - 1, Character: byteToChar(last, len(last))}
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// wordAt returns the identifier surrounding byte offset col in line, with its
// byte bounds. col is snapped to a rune boundary.
func wordAt(line string, col int) (string, int, int) {
	if col > len(line) {
		col = len(line)
	}
	for col > 0 && col < len(line) && !utf8.RuneStart(line[col]) {
		col--
	}
	if col < 0 {
		col = 0
	}
	if col < len(line) {
		r, _ := utf8.DecodeRuneInString(line[col:])
		if !isWordRune(r) {
			return "", col, col
		}
	}
	start := col
	for start > 0 {
		r, size := utf8.DecodeLastRuneInString(line[:start])
		if !isWordRune(r) {
			break
		}
		start -= size
	}
	end := col
	for end < len(line) {
		r, size := utf8.DecodeRuneInString(line[end:])
		if !isWordRune(r) {
			break
		}
		end += size
	}
	return line[start:end], start, end
}

// applyRange replaces the UTF-16 delimited range r in text with newText.
func applyRange(text string, r lspRange, newText string) string {
	lines := strings.SplitAfter(text, "\n")
	start := offsetAt(lines, r.Start.Line, r.Start.Character)
	end := offsetAt(lines, r.End.Line, r.End.Character)
	if start < 0 {
		start = 0
	}
	if end > len(text) {
		end = len(text)
	}
	if start > end {
		start = end
	}
	return text[:start] + newText + text[end:]
}

func offsetAt(lines []string, line, char int) int {
	if line < 0 {
		line = 0
	}
	off := 0
	for i := 0; i < line && i < len(lines); i++ {
		off += len(lines[i])
	}
	if line < len(lines) {
		content := strings.TrimSuffix(lines[line], "\n")
		content = strings.TrimSuffix(content, "\r")
		off += charToByte(content, char)
	}
	return off
}
