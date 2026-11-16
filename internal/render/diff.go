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
func Diff(oldText, newText string, context int) []Hunk {
	if oldText == newText {
		return nil
	}
	if context < 0 {
		context = 0
	}
	oldLines, newLines := splitLines(oldText), splitLines(newText)
	script := editScript(oldLines, newLines)
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

// editScript returns a shortest edit script turning a into b, with every run of
// changes written as its removals followed by its additions.
//
// It runs on the router, on every Changes page load, over a bird.conf that
// IRR-expanded prefix sets can push past 20,000 lines — so it must not cost
// what a textbook LCS table does, (n+1)×(m+1) ints, about 3 GB at that size.
// Instead: lines are interned to ints; the common head and tail, which is
// nearly all of a config between two renders, are matched outright; lines only
// one side has are set aside, since they can never match; and what is left is
// aligned with Myers' O((n+m)·d) algorithm in linear space. Memory is
// proportional to the two files, and time to their size times the number of
// differing lines.
func editScript(a, b []string) []edit {
	ids := make(map[string]int, len(a))
	intern := func(lines []string) []int {
		out := make([]int, len(lines))
		for i, l := range lines {
			id, ok := ids[l]
			if !ok {
				id = len(ids)
				ids[l] = id
			}
			out[i] = id
		}
		return out
	}
	x, y := intern(a), intern(b)

	pre := 0
	for pre < len(x) && pre < len(y) && x[pre] == y[pre] {
		pre++
	}
	suf := 0
	for suf < len(x)-pre && suf < len(y)-pre && x[len(x)-1-suf] == y[len(y)-1-suf] {
		suf++
	}
	xm, ym := x[pre:len(x)-suf], y[pre:len(y)-suf]

	// Keep only the lines the other side also has, remembering where each came
	// from. Dropping the rest cannot shorten a common subsequence: a line only
	// one file holds is an edit in every script. It is what keeps two mostly
	// different files — a hand-written config before adoption — from costing
	// the worst case.
	inX := make(map[int]bool, len(xm))
	for _, id := range xm {
		inX[id] = true
	}
	inY := make(map[int]bool, len(ym))
	for _, id := range ym {
		inY[id] = true
	}
	var xs, ys, xAt, yAt []int
	for i, id := range xm {
		if inY[id] {
			xs, xAt = append(xs, id), append(xAt, i)
		}
	}
	for j, id := range ym {
		if inX[id] {
			ys, yAt = append(ys, id), append(yAt, j)
		}
	}

	out := make([]edit, 0, len(a)+len(b)-pre-suf)
	for k := range pre {
		out = append(out, edit{' ', a[k], k + 1, k + 1})
	}
	i, j := pre, pre // next line of a and of b not yet written
	advance := func(toA, toB int) {
		for ; i < toA; i++ {
			out = append(out, edit{'-', a[i], i + 1, 0})
		}
		for ; j < toB; j++ {
			out = append(out, edit{'+', b[j], 0, j + 1})
		}
	}
	for _, m := range matchLines(xs, ys, 0, 0, nil) {
		mi, mj := pre+xAt[m[0]], pre+yAt[m[1]]
		advance(mi, mj)
		out = append(out, edit{' ', a[mi], mi + 1, mj + 1})
		i, j = mi+1, mj+1
	}
	advance(len(a)-suf, len(b)-suf)
	for k := range suf {
		mi, mj := len(a)-suf+k, len(b)-suf+k
		out = append(out, edit{' ', a[mi], mi + 1, mj + 1})
	}
	return out
}

// matchLines appends to out, in order, the index pairs of a longest common
// subsequence of a and b, offset by (ao, bo). It splits the problem at the
// middle snake of a shortest edit path and recurses on each half, so it never
// holds more than two diagonal vectors at a time.
func matchLines(a, b []int, ao, bo int, out [][2]int) [][2]int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		out = append(out, [2]int{ao + n, bo + n})
		n++
	}
	a, b, ao, bo = a[n:], b[n:], ao+n, bo+n
	s := 0
	for s < len(a) && s < len(b) && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	a, b = a[:len(a)-s], b[:len(b)-s]
	if len(a) > 0 && len(b) > 0 {
		if x, y, ok := middleSnake(a, b); ok {
			out = matchLines(a[:x], b[:y], ao, bo, out)
			out = matchLines(a[x:], b[y:], ao+x, bo+y, out)
		}
	}
	for k := range s {
		out = append(out, [2]int{ao + len(a) + k, bo + len(b) + k})
	}
	return out
}

// middleSnake finds a point (x, y) that lies on a shortest edit path from (0, 0)
// to (len(a), len(b)) by running Myers' greedy search forward from the start and
// backward from the end until the two meet (Myers 1986, §4b; the bookkeeping
// follows Neil Fraser's diff-match-patch bisect). Both halves either side of the
// point need fewer edits than the whole, so recursing on them terminates. ok is
// false when a and b have nothing in common. The caller has already stripped
// any common head and tail, and neither slice is empty.
func middleSnake(a, b []int) (x, y int, ok bool) {
	n, m := len(a), len(b)
	maxD := (n + m + 1) / 2
	// Two spare slots: the search starts from off+1, which is past 2·maxD when
	// both slices hold a single line.
	off, size := maxD, 2*maxD+2
	// v1[off+k] is the furthest x reached on diagonal k going forward, v2 the
	// same going backward from the end, measured from the end.
	v1, v2 := make([]int, size), make([]int, size)
	for i := range v1 {
		v1[i], v2[i] = -1, -1
	}
	v1[off+1], v2[off+1] = 0, 0
	delta := n - m
	// With an odd delta the forward search is the one that meets the backward.
	front := delta%2 != 0
	// Diagonals that ran off the grid are not searched again.
	k1start, k1end, k2start, k2end := 0, 0, 0, 0
	for d := range maxD {
		for k1 := -d + k1start; k1 <= d-k1end; k1 += 2 {
			k1o := off + k1
			var x1 int
			if k1 == -d || (k1 != d && v1[k1o-1] < v1[k1o+1]) {
				x1 = v1[k1o+1]
			} else {
				x1 = v1[k1o-1] + 1
			}
			y1 := x1 - k1
			for x1 < n && y1 < m && a[x1] == b[y1] {
				x1++
				y1++
			}
			v1[k1o] = x1
			switch {
			case x1 > n:
				k1end += 2
			case y1 > m:
				k1start += 2
			case front:
				if k2o := off + delta - k1; k2o >= 0 && k2o < size && v2[k2o] != -1 && x1 >= n-v2[k2o] {
					return x1, y1, true
				}
			}
		}
		for k2 := -d + k2start; k2 <= d-k2end; k2 += 2 {
			k2o := off + k2
			var x2 int
			if k2 == -d || (k2 != d && v2[k2o-1] < v2[k2o+1]) {
				x2 = v2[k2o+1]
			} else {
				x2 = v2[k2o-1] + 1
			}
			y2 := x2 - k2
			for x2 < n && y2 < m && a[n-x2-1] == b[m-y2-1] {
				x2++
				y2++
			}
			v2[k2o] = x2
			switch {
			case x2 > n:
				k2end += 2
			case y2 > m:
				k2start += 2
			case !front:
				if k1o := off + delta - k2; k1o >= 0 && k1o < size && v1[k1o] != -1 {
					x1 := v1[k1o]
					if x1 >= n-x2 {
						return x1, x1 - (k1o - off), true
					}
				}
			}
		}
	}
	return 0, 0, false
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

	script := editScript(oldLines, newLines)

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
