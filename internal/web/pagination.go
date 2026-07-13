package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

const defaultPageSize = 50

// parsePageParams reads "offset"/"limit" query params shared by every
// paginated route listing (peer detail tabs, looking glass).
func parsePageParams(r *http.Request) (offset, limit int) {
	return parsePageParamsNamed(r, "offset")
}

// parsePageParamsNamed is parsePageParams for a table that cannot own the plain
// "offset" — the policies page shows imports and exports side by side, and paging
// one must not move the other.
func parsePageParamsNamed(r *http.Request, param string) (offset, limit int) {
	offset, _ = strconv.Atoi(r.URL.Query().Get(param))
	if offset < 0 {
		offset = 0
	}
	limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = defaultPageSize
	}
	return offset, limit
}

// Pager is the paging state of one table, and the links to move through it. Every
// table in birdy renders it through the same "pager" template, so they cannot
// drift into having different controls.
//
// Two kinds of table use it. A database listing knows its Total, so the pager can
// number every page and jump to the last. A route listing streamed from BIRD does
// not — birdy deliberately never counts a 2.6M-route table to draw a pager — so
// Total is TotalUnknown and it offers the pages it can prove exist, plus Next.
type Pager struct {
	Offset  int
	Limit   int
	Shown   int // rows on this page
	Total   int // TotalUnknown when the total cannot be counted cheaply
	HasMore bool

	path  string
	param string // the query parameter carrying this table's offset
	query url.Values
}

// TotalUnknown marks a page count birdy will not pay to compute.
const TotalUnknown = -1

// PageLink is one control in the pager: a numbered page, or an ellipsis standing
// in for the pages it would be useless to list.
type PageLink struct {
	Label    string
	URL      string
	Current  bool
	Ellipsis bool
}

// newPager builds the pager for a request. Every query parameter except offset is
// carried into the links, so paging never loses the looking-glass target, the
// active tab, or a filter.
func newPager(r *http.Request, offset, limit, shown, total int, hasMore bool) Pager {
	return newPagerNamed(r, "offset", offset, limit, shown, total, hasMore)
}

func newPagerNamed(r *http.Request, param string, offset, limit, shown, total int, hasMore bool) Pager {
	q := url.Values{}
	for k, vs := range r.URL.Query() {
		if k == param {
			continue // this table's own offset is rewritten per link
		}
		q[k] = vs
	}
	return Pager{
		Offset: offset, Limit: limit, Shown: shown, Total: total, HasMore: hasMore,
		path: r.URL.Path, param: param, query: q,
	}
}

// pagerFor is newPager for an in-memory listing: it takes the full slice length as
// the total and works out what is on this page.
func pagerFor(r *http.Request, offset, limit, shown, total int) Pager {
	return newPager(r, offset, limit, shown, total, offset+shown < total)
}

// pagerForNamed is pagerFor on a page with more than one paginated table.
func pagerForNamed(r *http.Request, param string, offset, limit, shown, total int) Pager {
	return newPagerNamed(r, param, offset, limit, shown, total, offset+shown < total)
}

// pageSlice cuts one page out of a list already held in memory. Small tables — the
// library, peers, policies — are read whole and paged here rather than in SQL: the
// query is cheap, and a COUNT plus a LIMIT/OFFSET for twenty rows is not worth the
// round trips.
func pageSlice[T any](items []T, offset, limit int) []T {
	if offset >= len(items) {
		return nil
	}
	end := min(offset+limit, len(items))
	return items[offset:end]
}

// Show reports whether the pager is worth drawing at all. A table that fits on one
// page gets no controls — pagination you cannot use is furniture.
func (p Pager) Show() bool {
	return p.Offset > 0 || p.HasMore || (p.Total > 0 && p.Total > p.Limit)
}

func (p Pager) TotalKnown() bool { return p.Total != TotalUnknown }

// FirstRow / LastRow are 1-based and inclusive; zero on an empty page. Templates
// cannot do arithmetic, so it happens here.
func (p Pager) FirstRow() int {
	if p.Shown == 0 {
		return 0
	}
	return p.Offset + 1
}

func (p Pager) LastRow() int {
	if p.Shown == 0 {
		return 0
	}
	return p.Offset + p.Shown
}

// Page is the 1-based number of the page being shown.
func (p Pager) Page() int {
	if p.Limit <= 0 {
		return 1
	}
	return p.Offset/p.Limit + 1
}

// TotalPages is 0 when the total is unknown.
func (p Pager) TotalPages() int {
	if !p.TotalKnown() || p.Limit <= 0 {
		return 0
	}
	return max(1, (p.Total+p.Limit-1)/p.Limit)
}

func (p Pager) link(page int) string {
	q := url.Values{}
	for k, vs := range p.query {
		q[k] = vs
	}
	param := p.param
	if param == "" {
		param = "offset"
	}
	q.Set(param, strconv.Itoa((page-1)*p.Limit))
	q.Set("limit", strconv.Itoa(p.Limit))
	return p.path + "?" + q.Encode()
}

// PrevLink / NextLink are empty when there is nowhere to go, so the template can
// simply test them.
func (p Pager) PrevLink() string {
	if p.Page() <= 1 {
		return ""
	}
	return p.link(p.Page() - 1)
}

func (p Pager) NextLink() string {
	if !p.HasMore {
		return ""
	}
	return p.link(p.Page() + 1)
}

// pagerWindow is how many numbered pages sit either side of the current one before
// an ellipsis takes over.
const pagerWindow = 2

// Links is the numbered page selector: first page, a window around the current
// one, the last page, and ellipses for the gaps. Jumping straight to a page is the
// thing prev/next cannot do, and the reason this exists.
//
// When the total is unknown, the last page is unknowable too, so it offers what it
// can prove: page 1 through the next page, and no jump to the end.
func (p Pager) Links() []PageLink {
	cur, last := p.Page(), p.TotalPages()
	if last == 0 { // unknown total: we know pages up to the next one exists
		last = cur
		if p.HasMore {
			last = cur + 1
		}
	}
	if last <= 1 {
		return nil
	}

	want := map[int]bool{1: true, last: true, cur: true}
	for i := 1; i <= pagerWindow; i++ {
		if cur-i >= 1 {
			want[cur-i] = true
		}
		if cur+i <= last {
			want[cur+i] = true
		}
	}

	var out []PageLink
	prev := 0
	for n := 1; n <= last; n++ {
		if !want[n] {
			continue
		}
		if prev != 0 && n != prev+1 {
			out = append(out, PageLink{Ellipsis: true, Label: "…"})
		}
		out = append(out, PageLink{Label: strconv.Itoa(n), URL: p.link(n), Current: n == cur})
		prev = n
	}
	return out
}

// Summary is the "rows 51–100 of 240" line. With an unknown total it says what it
// knows and no more.
func (p Pager) Summary() string {
	if p.Shown == 0 {
		return "no rows"
	}
	if p.TotalKnown() {
		return fmt.Sprintf("rows %d–%d of %s", p.FirstRow(), p.LastRow(), comma(p.Total))
	}
	if p.HasMore {
		return fmt.Sprintf("rows %d–%d · more available", p.FirstRow(), p.LastRow())
	}
	return fmt.Sprintf("rows %d–%d", p.FirstRow(), p.LastRow())
}
