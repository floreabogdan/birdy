package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func req(t *testing.T, url string) *Pager {
	t.Helper()
	r := httptest.NewRequest("GET", url, nil)
	offset, limit := parsePageParams(r)
	p := pagerFor(r, offset, limit, min(limit, 240-offset), 240)
	return &p
}

// The point of the numbered pages: jump straight to a page instead of clicking
// Next eleven times.
func TestPagerNumbersPagesWithEllipses(t *testing.T) {
	p := req(t, "/timeline?offset=250&limit=50") // page 6 of 5... clamp check below
	p2 := req(t, "/timeline?offset=200&limit=50")

	if p2.Page() != 5 || p2.TotalPages() != 5 {
		t.Fatalf("page %d of %d, want 5 of 5", p2.Page(), p2.TotalPages())
	}
	if p.Page() != 6 {
		t.Errorf("offset past the end should still report its page, got %d", p.Page())
	}

	// A wide table: current page in the middle, ellipses either side, first and
	// last always reachable.
	r := httptest.NewRequest("GET", "/timeline?offset=500&limit=10", nil)
	off, lim := parsePageParams(r)
	wide := pagerFor(r, off, lim, 10, 1000) // page 51 of 100
	var labels []string
	for _, l := range wide.Links() {
		labels = append(labels, l.Label)
	}
	got := strings.Join(labels, " ")
	want := "1 … 49 50 51 52 53 … 100"
	if got != want {
		t.Errorf("page links = %q, want %q", got, want)
	}
	for _, l := range wide.Links() {
		if l.Label == "51" && !l.Current {
			t.Error("the current page should be marked current")
		}
		if l.Label == "100" && !strings.Contains(l.URL, "offset=990") {
			t.Errorf("the last page should link to the last offset, got %q", l.URL)
		}
	}
}

// A table that fits on one page gets no controls at all — pagination you cannot
// use is furniture.
func TestPagerHiddenWhenEverythingFits(t *testing.T) {
	r := httptest.NewRequest("GET", "/peers", nil)
	off, lim := parsePageParams(r)
	p := pagerFor(r, off, lim, 3, 3)
	if p.Show() {
		t.Error("three rows on a 50-row page should not draw a pager")
	}
	if p.Links() != nil {
		t.Error("no page links either")
	}
}

// A BIRD route table is never counted (millions of routes), so the pager offers
// the pages it can prove exist and no jump to a last page it cannot know.
func TestPagerWithUnknownTotal(t *testing.T) {
	r := httptest.NewRequest("GET", "/lg?type=for&target=192.0.2.0%2F24&offset=100&limit=50", nil)
	off, lim := parsePageParams(r)
	p := newPager(r, off, lim, 50, TotalUnknown, true)

	if p.TotalKnown() || p.TotalPages() != 0 {
		t.Error("the total must stay unknown")
	}
	if !strings.Contains(p.Summary(), "more available") {
		t.Errorf("summary should admit it does not know the total, got %q", p.Summary())
	}
	var last string
	for _, l := range p.Links() {
		last = l.Label
	}
	if last != "4" { // on page 3, page 4 is provably next
		t.Errorf("last offered page = %q, want 4 (current + next)", last)
	}
	// The query it was paging must survive: losing the target would reset the search.
	if !strings.Contains(p.NextLink(), "target=192.0.2.0%2F24") {
		t.Errorf("paging must preserve the query, got %q", p.NextLink())
	}
}

// Two paginated tables on one page must not drag each other around.
func TestPagerNamedParamsAreIndependent(t *testing.T) {
	r := httptest.NewRequest("GET", "/policies?offset=50&eoffset=100", nil)
	iOff, limit := parsePageParamsNamed(r, "offset")
	eOff, _ := parsePageParamsNamed(r, "eoffset")
	imports := pagerForNamed(r, "offset", iOff, limit, 50, 300)
	exports := pagerForNamed(r, "eoffset", eOff, limit, 50, 300)

	if imports.Page() != 2 || exports.Page() != 3 {
		t.Fatalf("imports on page %d, exports on page %d; want 2 and 3", imports.Page(), exports.Page())
	}
	// Paging the imports keeps the exports where they were.
	next := imports.NextLink()
	if !strings.Contains(next, "offset=100") || !strings.Contains(next, "eoffset=100") {
		t.Errorf("paging imports must preserve the exports' offset, got %q", next)
	}
}
