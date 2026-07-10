package render

import (
	"fmt"
	"strings"
)

// DiffLine is one line of a unified diff. Op is ' ', '-' or '+'.
type DiffLine struct {
	Op   byte
	Text string
}

// Hunk is a contiguous run of changes plus its surrounding context.
type Hunk struct {
	OldStart, OldCount int
	NewStart, NewCount int
	Lines              []DiffLine
}

// Header renders the @@ line.
func (h Hunk) Header() string {
	return fmt.Sprintf("@@ -%d,%d +%d,%d @@", h.OldStart, h.OldCount, h.NewStart, h.NewCount)
}

// Diff computes a unified diff between old and new with the given amount of
// context. An empty result means the two texts are identical.
//
// birdy diffs whole config files that are mostly identical between renders, so
// a plain O(n*m) LCS is fine: a bird.conf is thousands of lines, not millions.
func Diff(oldText, newText string, context int) []Hunk {
	if oldText == newText {
		return nil
	}
	if context < 0 {
		context = 0
	}
	oldLines, newLines := splitLines(oldText), splitLines(newText)
	script := lcsScript(oldLines, newLines)
	return hunks(script, context)
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

// edit is one entry of the full edit script, carrying the 1-based line numbers
// the line occupies in the old and new files (0 when it exists in neither).
type edit struct {
	op      byte
	text    string
	oldLine int
	newLine int
}

func lcsScript(a, b []string) []edit {
	n, m := len(a), len(b)
	// lengths[i][j] = length of the LCS of a[i:] and b[j:]
	lengths := make([][]int, n+1)
	for i := range lengths {
		lengths[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lengths[i][j] = lengths[i+1][j+1] + 1
			} else {
				lengths[i][j] = max(lengths[i+1][j], lengths[i][j+1])
			}
		}
	}

	var out []edit
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, edit{' ', a[i], i + 1, j + 1})
			i++
			j++
		case lengths[i+1][j] >= lengths[i][j+1]:
			out = append(out, edit{'-', a[i], i + 1, 0})
			i++
		default:
			out = append(out, edit{'+', b[j], 0, j + 1})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, edit{'-', a[i], i + 1, 0})
	}
	for ; j < m; j++ {
		out = append(out, edit{'+', b[j], 0, j + 1})
	}
	return out
}

// hunks groups the edit script into unified-diff hunks, keeping `context`
// unchanged lines around each run of changes and dropping the rest.
func hunks(script []edit, context int) []Hunk {
	changed := make([]bool, len(script))
	any := false
	for i, e := range script {
		if e.op != ' ' {
			changed[i] = true
			any = true
		}
	}
	if !any {
		return nil
	}

	keep := make([]bool, len(script))
	for i, c := range changed {
		if !c {
			continue
		}
		for j := max(0, i-context); j <= min(len(script)-1, i+context); j++ {
			keep[j] = true
		}
	}

	var out []Hunk
	for i := 0; i < len(script); {
		if !keep[i] {
			i++
			continue
		}
		j := i
		for j < len(script) && keep[j] {
			j++
		}
		out = append(out, buildHunk(script[i:j]))
		i = j
	}
	return out
}

func buildHunk(seg []edit) Hunk {
	h := Hunk{}
	for _, e := range seg {
		h.Lines = append(h.Lines, DiffLine{Op: e.op, Text: e.text})
		if e.op != '+' {
			if h.OldStart == 0 {
				h.OldStart = e.oldLine
			}
			h.OldCount++
		}
		if e.op != '-' {
			if h.NewStart == 0 {
				h.NewStart = e.newLine
			}
			h.NewCount++
		}
	}
	// A hunk that is pure insertion has no old line to anchor to, and vice
	// versa; unified diff writes the position it would be inserted after.
	if h.OldStart == 0 && h.OldCount == 0 {
		h.OldStart = 0
	}
	if h.NewStart == 0 && h.NewCount == 0 {
		h.NewStart = 0
	}
	return h
}

// Stat counts added and removed lines across all hunks.
func Stat(hs []Hunk) (added, removed int) {
	for _, h := range hs {
		for _, l := range h.Lines {
			switch l.Op {
			case '+':
				added++
			case '-':
				removed++
			}
		}
	}
	return added, removed
}
