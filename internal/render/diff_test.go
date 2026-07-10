package render

import (
	"strings"
	"testing"
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
