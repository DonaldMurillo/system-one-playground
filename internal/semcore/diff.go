package semcore

import (
	"bufio"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ChangedLines maps an absolute file path to the line ranges a diff touched.
// Linting only the units that overlap these ranges is what makes the tool
// usable in CI: a package with thirteen thousand units has a handful of
// changed ones after a normal commit.
type ChangedLines map[string][]LineRange

// LineRange is an inclusive span of 1-indexed lines.
type LineRange struct{ Start, End int }

func (r LineRange) overlaps(start, end int) bool { return start <= r.End && end >= r.Start }

// Touches reports whether any changed range overlaps the given span.
func (c ChangedLines) Touches(path string, start, end int) bool {
	ranges, ok := c[path]
	if !ok {
		return false
	}
	for _, r := range ranges {
		if r.overlaps(start, end) {
			return true
		}
	}
	return false
}

// Files lists the paths the diff touched.
func (c ChangedLines) Files() []string {
	out := make([]string, 0, len(c))
	for p := range c {
		out = append(out, p)
	}
	return out
}

var (
	reDiffTarget = regexp.MustCompile(`^\+\+\+ (?:b/)?(.+?)\s*$`)
	reDiffHunk   = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)
)

// ParseUnifiedDiff reads a unified diff and returns the lines added or changed
// on the new side. Deletions are deliberately ignored: there is no code left to
// judge where a line was removed.
func ParseUnifiedDiff(r io.Reader, root string) (ChangedLines, error) {
	out := ChangedLines{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)

	var current string
	var newLine int
	inHunk := false

	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "+++ "):
			inHunk = false
			m := reDiffTarget.FindStringSubmatch(line)
			if m == nil || m[1] == "/dev/null" {
				current = ""
				continue
			}
			current = absUnder(root, m[1])
		case strings.HasPrefix(line, "@@"):
			m := reDiffHunk.FindStringSubmatch(line)
			if m == nil || current == "" {
				inHunk = false
				continue
			}
			start, _ := strconv.Atoi(m[1])
			newLine = start
			inHunk = true
		case inHunk && current != "":
			switch {
			case strings.HasPrefix(line, "+"):
				out[current] = appendRange(out[current], newLine)
				newLine++
			case strings.HasPrefix(line, "-"):
				// Removed from the old side; the new side does not advance.
			case strings.HasPrefix(line, "\\"):
				// "\ No newline at end of file"
			default:
				newLine++ // context line
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// appendRange adds a line, merging it into the previous range when adjacent.
func appendRange(rs []LineRange, line int) []LineRange {
	if n := len(rs); n > 0 && line <= rs[n-1].End+1 {
		if line > rs[n-1].End {
			rs[n-1].End = line
		}
		return rs
	}
	return append(rs, LineRange{line, line})
}

func absUnder(root, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(root, p))
}
