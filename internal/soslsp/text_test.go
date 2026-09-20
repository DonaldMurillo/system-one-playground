package soslsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

const utf16Line = "héllo🎁w" // h(1B/1u) é(2B/1u) l l o(1B each) 🎁(4B/2u) w(1B/1u)

func TestByteCharConversions(t *testing.T) {
	// Byte offsets: h=0, é=1, l=3, l=4, o=5, 🎁=6, w=10, len=11.
	cases := []struct {
		byteCol, char int
	}{
		{0, 0}, {1, 1}, {3, 2}, {4, 3}, {5, 4}, {6, 5}, {10, 7}, {11, 8},
	}
	for _, c := range cases {
		if got := byteToChar(utf16Line, c.byteCol); got != c.char {
			t.Fatalf("byteToChar(%d) = %d, want %d", c.byteCol, got, c.char)
		}
		if got := charToByte(utf16Line, c.char); got != c.byteCol {
			t.Fatalf("charToByte(%d) = %d, want %d", c.char, got, c.byteCol)
		}
	}
	// Clamping and surrogate splitting.
	if got := byteToChar(utf16Line, 99); got != 8 {
		t.Fatalf("byteToChar clamp = %d, want 8", got)
	}
	if got := charToByte(utf16Line, 6); got != 10 {
		t.Fatalf("charToByte inside surrogate pair = %d, want next rune at 10", got)
	}
	if got := charToByte(utf16Line, 99); got != len(utf16Line) {
		t.Fatalf("charToByte clamp = %d, want %d", got, len(utf16Line))
	}
	if got := charToByte(utf16Line, -3); got != 0 {
		t.Fatalf("charToByte negative = %d, want 0", got)
	}
}

func TestCharWidthAt(t *testing.T) {
	if got := charWidthAt(utf16Line, 6); got != 2 {
		t.Fatalf("non-BMP width = %d, want 2", got)
	}
	if got := charWidthAt(utf16Line, 1); got != 1 {
		t.Fatalf("BMP width = %d, want 1", got)
	}
	if got := charWidthAt(utf16Line, len(utf16Line)); got != 0 {
		t.Fatalf("end of line width = %d, want 0", got)
	}
}

func TestWordAt(t *testing.T) {
	word, start, end := wordAt("show reports as table", 5)
	if word != "reports" || start != 5 || end != 12 {
		t.Fatalf("wordAt = %q [%d,%d), want reports [5,12)", word, start, end)
	}
	word, start, end = wordAt("make café now", len("make café "))
	if word != "now" || start != len("make café ") {
		t.Fatalf("unicode adjacent word = %q [%d,%d)", word, start, end)
	}
	if word, _, _ := wordAt("show reports", 4); word != "" {
		t.Fatalf("word at space should be empty, got %q", word)
	}
}

func TestLineAtAndEndPosition(t *testing.T) {
	src := "a\nbét\r\nc"
	if got := lineAt(src, 1); got != "bét" {
		t.Fatalf("lineAt CRLF = %q", got)
	}
	if got := lineAt(src, 9); got != "c" {
		t.Fatalf("lineAt clamp = %q", got)
	}
	end := endPosition(src)
	if end.Line != 2 || end.Character != 1 {
		t.Fatalf("endPosition = %+v, want line 2 char 1", end)
	}
	single := endPosition("héllo🎁")
	if single.Line != 0 || single.Character != 7 {
		t.Fatalf("single line endPosition = %+v, want char 7", single)
	}
}

func TestApplyRange(t *testing.T) {
	text := "héllo world\nsecond"
	got := applyRange(text, lspRange{
		Start: lspPosition{Line: 0, Character: 6},
		End:   lspPosition{Line: 0, Character: 11},
	}, "sos")
	if got != "héllo sos\nsecond" {
		t.Fatalf("applyRange = %q", got)
	}
	got = applyRange(text, lspRange{
		Start: lspPosition{Line: 0, Character: 6},
		End:   lspPosition{Line: 1, Character: 3},
	}, "X")
	if got != "héllo Xond" {
		t.Fatalf("cross line applyRange = %q", got)
	}
}

func TestReadFrame(t *testing.T) {
	br := bufio.NewReader(strings.NewReader("Content-Length: 2\r\nContent-Type: application/vscode-jsonrpc\r\n\r\n{}extra"))
	body, err := readFrame(br)
	if err != nil || string(body) != "{}" {
		t.Fatalf("readFrame = %q, %v", body, err)
	}
	if b, _ := br.ReadByte(); b != 'e' {
		t.Fatalf("reader not positioned after body")
	}

	_, err = readFrame(bufio.NewReader(strings.NewReader("Content-Length: -1\r\n\r\n")))
	if err == nil {
		t.Fatal("negative Content-Length must fail")
	}
	_, err = readFrame(bufio.NewReader(strings.NewReader("Content-Type: x\r\n\r\n{}")))
	if err == nil || !strings.Contains(err.Error(), "Content-Length") {
		t.Fatalf("missing Content-Length must fail, got %v", err)
	}
	_, err = readFrame(bufio.NewReader(strings.NewReader("Content-Length: " + strings.Repeat("9", maxFrameBytes+1) + "\r\n\r\n")))
	if err == nil {
		t.Fatal("absurd Content-Length must fail")
	}
	var huge bytes.Buffer
	huge.WriteString("X-Token: " + strings.Repeat("a", 5000) + "\r\nContent-Length: 1\r\n\r\n{}")
	_, err = readFrame(bufio.NewReaderSize(&huge, maxHeaderLine))
	if err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("long header must fail, got %v", err)
	}
	_, err = readFrame(bufio.NewReader(strings.NewReader("Content-Length: 10\r\n\r\nshort")))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated body must yield ErrUnexpectedEOF, got %v", err)
	}
}

func TestWriteMessageFraming(t *testing.T) {
	var buf bytes.Buffer
	s := &server{out: &buf}
	s.writeResult(json.RawMessage("9"), map[string]any{"ok": true})
	out := buf.String()
	if !strings.HasPrefix(out, "Content-Length: ") || !strings.Contains(out, `"result":{"ok":true}`) {
		t.Fatalf("bad framing: %q", out)
	}
}
