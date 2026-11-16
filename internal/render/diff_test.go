package render

import (
	"fmt"
	"math/rand/v2"
	"runtime"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

func TestDiffIdentical(t *testing.T) {
	if hs := Diff("a\nb\nc\n", "a\nb\nc\n", 3); hs != nil {
		t.Errorf("identical texts must produce no hunks, got %d", len(hs))
	}
	if hs := Diff("", "", 3); hs != nil {
		t.Error("empty texts must produce no hunks")
	}
}

func TestDiffFromEmpty(t *testing.T) {
	hs := Diff("", "line one\nline two\n", 3)
	added, removed := Stat(hs)
	if added != 2 || removed != 0 {
		t.Errorf("got +%d -%d, want +2 -0", added, removed)
	}
}

func TestDiffToEmpty(t *testing.T) {
	hs := Diff("line one\nline two\n", "", 3)
	added, removed := Stat(hs)
	if added != 0 || removed != 2 {
		t.Errorf("got +%d -%d, want +0 -2", added, removed)
	}
}

func TestDiffContextLimitsHunks(t *testing.T) {
	// Two edits far apart should not be merged into one hunk at low context.
	var oldB, newB strings.Builder
	for i := range 40 {
		oldB.WriteString("line\n")
		if i == 2 || i == 30 {
			newB.WriteString("changed\n")
		} else {
			newB.WriteString("line\n")
		}
	}
	hs := Diff(oldB.String(), newB.String(), 2)
	if len(hs) != 2 {
		t.Errorf("want 2 hunks with context=2, got %d", len(hs))
	}
	// With enough context the two runs overlap and collapse into one hunk.
	if hs := Diff(oldB.String(), newB.String(), 20); len(hs) != 1 {
		t.Errorf("want 1 hunk with context=20, got %d", len(hs))
	}
}

// The diff is only trustworthy if replaying it on the old text yields the new
// text. Reconstruct from a full-context diff, which contains every line.
func TestDiffReconstructs(t *testing.T) {
	cases := []struct{ name, oldText, newText string }{
		{"insert middle", "a\nb\ne\n", "a\nb\nc\nd\ne\n"},
		{"delete middle", "a\nb\nc\nd\ne\n", "a\ne\n"},
		{"replace all", "a\nb\n", "x\ny\n"},
		{"prepend", "b\nc\n", "a\nb\nc\n"},
		{"append", "a\nb\n", "a\nb\nc\n"},
		{"duplicate lines", "x\nx\nx\n", "x\nx\n"},
		{"config-like", "protocol bgp a {\n\timport all;\n}\n", "protocol bgp a {\n\timport filter f;\n\texport none;\n}\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hs := Diff(c.oldText, c.newText, 1<<30)
			var gotOld, gotNew []string
			for _, h := range hs {
				for _, l := range h.Lines {
					switch l.Op {
					case ' ':
						gotOld = append(gotOld, l.Text)
						gotNew = append(gotNew, l.Text)
					case '-':
						gotOld = append(gotOld, l.Text)
					case '+':
						gotNew = append(gotNew, l.Text)
					}
				}
			}
			wantOld := strings.Join(splitLines(c.oldText), "\n")
			wantNew := strings.Join(splitLines(c.newText), "\n")
			if strings.Join(gotOld, "\n") != wantOld {
				t.Errorf("old side mismatch:\n got %q\nwant %q", strings.Join(gotOld, "\n"), wantOld)
			}
			if strings.Join(gotNew, "\n") != wantNew {
				t.Errorf("new side mismatch:\n got %q\nwant %q", strings.Join(gotNew, "\n"), wantNew)
			}
		})
	}
}

func TestHunkHeaderCounts(t *testing.T) {
	hs := Diff("a\nb\nc\n", "a\nB\nc\n", 1)
	if len(hs) != 1 {
		t.Fatalf("want 1 hunk, got %d", len(hs))
	}
	h := hs[0]
	if h.OldStart != 1 || h.NewStart != 1 {
		t.Errorf("hunk should start at line 1, got old=%d new=%d", h.OldStart, h.NewStart)
	}
	if h.OldCount != 3 || h.NewCount != 3 {
		t.Errorf("want 3 old / 3 new lines, got %d/%d", h.OldCount, h.NewCount)
	}
	if got := h.Header(); got != "@@ -1,3 +1,3 @@" {
		t.Errorf("header = %q", got)
	}
}

func TestCRLFNormalised(t *testing.T) {
	if hs := Diff("a\r\nb\r\n", "a\nb\n", 3); hs != nil {
		t.Error("line-ending style alone must not register as a change")
	}
}

// allocated reports how many bytes f allocates on the heap.
func allocated(f func()) uint64 {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// configLines is a bird.conf-sized text: an IRR-expanded prefix set is
// thousands of lines that differ only a little between renders.
func configLines(n int, changed ...int) string {
	var b strings.Builder
	for i := range n {
		if slicesContains(changed, i) {
			fmt.Fprintf(&b, "\t# changed line %d\n", i)
			continue
		}
		fmt.Fprintf(&b, "\t10.%d.%d.0/24{24,24},\n", i/256, i%256)
	}
	return b.String()
}

func slicesContains(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// The Changes page diffs the running config against the candidate twice per
// load, changed or not, on the router itself. The LCS table this used to build
// was (n+1)×(m+1) ints — 125 MB for 4,000 lines with one change, about 3 GB at
// 20,000 — enough to get birdy killed. A diff costs memory in proportion to
// the files, however far apart the changes are: here one at each end, so
// trimming the common prefix and suffix alone does not help.
func TestDiffLargeConfigStaysSmall(t *testing.T) {
	for _, n := range []int{4000, 20000} {
		oldText := configLines(n)
		newText := configLines(n, 0, n/2, n-1)
		var hs []Hunk
		used := allocated(func() { hs = Diff(oldText, newText, 3) })
		if added, removed := Stat(hs); added != 3 || removed != 3 || len(hs) != 3 {
			t.Fatalf("%d lines: want 3 hunks of -1 +1, got %d hunks, +%d -%d", n, len(hs), added, removed)
		}
		if limit := uint64(n) * 1024; used > limit {
			t.Fatalf("%d lines: the diff allocated %d MB, want under %d MB", n, used>>20, limit>>20)
		}
	}
}

// Every Changes page load also attributes the diff to sections — even when the
// candidate is exactly what is running, the common case.
func TestSectionDiffUnchangedLargeConfigStaysSmall(t *testing.T) {
	in := baseInput()
	ps := store.PrefixSet{ID: 30, Name: "CUSTOMER_IRR", Family: store.FamilyV4}
	for i := range 4000 {
		ps.Entries = append(ps.Entries, store.PrefixEntry{Prefix: fmt.Sprintf("10.%d.%d.0/24", i/256, i%256)})
	}
	in.PrefixSets = []store.PrefixSet{ps}
	cfg := mustRender(t, in)
	render := allocated(func() { _, _ = Sections(in) })

	var fcs []FileChange
	var err error
	used := allocated(func() { fcs, err = SectionDiff(cfg, in, 3) })
	if err != nil {
		t.Fatal(err)
	}
	for _, fc := range fcs {
		if fc.Status != "unchanged" {
			t.Errorf("%s: an unchanged config reported %s", fc.Path, fc.Status)
		}
	}
	if limit := render + 8<<20; used > limit {
		t.Errorf("diffing an unchanged config allocated %d MB on top of rendering it", (used-render)>>20)
	}
}

// referenceLCS is the textbook O(n·m) dynamic programme the diff used to be,
// kept as the yardstick for the edit script's length.
func referenceLCS(a, b []string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				cur[j] = prev[j+1] + 1
			} else {
				cur[j] = max(prev[j], cur[j+1])
			}
		}
		prev, cur = cur, prev
	}
	return prev[0]
}

// The edit script must stay a shortest one, replay to both texts, and keep the
// unified-diff shape the UI renders: within a run of changes, every removed
// line before every added one.
func TestEditScriptIsMinimal(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	alphabet := []string{"}", "", "\tipv4 {", "a", "b", "c", "d", "e"}
	gen := func() []string {
		n := r.IntN(14)
		out := make([]string, n)
		for i := range out {
			out[i] = alphabet[r.IntN(len(alphabet))]
		}
		return out
	}
	for range 5000 {
		a, b := gen(), gen()
		script := editScript(a, b)
		var gotA, gotB []string
		equal := 0
		for k, e := range script {
			switch e.op {
			case ' ':
				gotA, gotB = append(gotA, e.text), append(gotB, e.text)
				equal++
			case '-':
				gotA = append(gotA, e.text)
				if k > 0 && script[k-1].op == '+' {
					t.Fatalf("a removal follows an addition in one run: %q -> %q: %+v", a, b, script)
				}
			case '+':
				gotB = append(gotB, e.text)
			}
		}
		if strings.Join(gotA, "\n") != strings.Join(a, "\n") || strings.Join(gotB, "\n") != strings.Join(b, "\n") {
			t.Fatalf("script does not replay: %q -> %q: %+v", a, b, script)
		}
		if want := referenceLCS(a, b); equal != want {
			t.Fatalf("%q -> %q: script keeps %d lines, a shortest one keeps %d", a, b, equal, want)
		}
	}
}
