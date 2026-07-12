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
	return h
}

// FileChange is the diff for one rendered section: how many lines it gained and
// lost, and the hunks scoped to it. Status is "added" (the whole unit is new),
// "removed" (the whole unit is gone), "modified", or "unchanged".
type FileChange struct {
	// Path is the section identifier, e.g. "peers/edge1"; File is the on-disk
	// file that section is written to under IncludeDir, e.g.
	// "birdy.d/09-peers-edge1.conf".
	Path    string
	File    string
	Title   string
	Status  string
	Added   int
	Removed int
	Hunks   []Hunk
}

// SectionDiff renders the model to sections, diffs the concatenated candidate
// against oldText, and attributes every changed line to the section it lands in
// — so the UI can show which unit of a large config each change touches.
//
// Attribution is by the candidate's line ownership: an added or unchanged line
// belongs to its own section; a deleted line (present only in oldText) is charged
// to the section whose lines surround it. That means a section removed outright
// from the model — its lines no longer exist in the candidate — shows up as
// deletions on the neighbouring section rather than as its own entry. Splitting
// the file physically (a later step) is what removes that seam.
func SectionDiff(oldText string, in Input, context int) ([]FileChange, error) {
	secs, err := Sections(in)
	if err != nil {
		return nil, err
	}

	var nb strings.Builder
	// lineOf[i] = index into secs of the section owning candidate line i (0-based).
	// Each body ends in a newline, so a section's line count is its newline count
	// and the partition lands exactly on section boundaries.
	var lineOf []int
	for si, s := range secs {
		nb.WriteString(s.Body)
		body := strings.ReplaceAll(s.Body, "\r\n", "\n")
		for k := 0; k < strings.Count(body, "\n"); k++ {
			lineOf = append(lineOf, si)
		}
	}

	oldLines, newLines := splitLines(oldText), splitLines(nb.String())
	// Guard against a body that did not end in a newline: keep ownership as long
	// as the line list, charging any tail to the last section.
	for len(lineOf) < len(newLines) && len(secs) > 0 {
		lineOf = append(lineOf, len(secs)-1)
	}

	script := lcsScript(oldLines, newLines)

	perSec := make([][]edit, len(secs))
	cur := 0
	for _, e := range script {
		si := cur
		if e.newLine > 0 && e.newLine-1 < len(lineOf) {
			si = lineOf[e.newLine-1]
			cur = si
		}
		if si >= 0 && si < len(perSec) {
			perSec[si] = append(perSec[si], e)
		}
	}

	out := make([]FileChange, 0, len(secs))
	for si, s := range secs {
		hs := hunks(perSec[si], context)
		added, removed := Stat(hs)
		hasEqual := false
		for _, e := range perSec[si] {
			if e.op == ' ' {
				hasEqual = true
				break
			}
		}
		fc := FileChange{Path: s.Path, File: includeFileName(s.Path), Title: s.Title, Added: added, Removed: removed, Hunks: hs}
		switch {
		case added == 0 && removed == 0:
			fc.Status = "unchanged"
		case removed == 0 && !hasEqual:
			fc.Status = "added"
		case added == 0 && !hasEqual:
			fc.Status = "removed"
		default:
			fc.Status = "modified"
		}
		out = append(out, fc)
	}
	return out, nil
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
