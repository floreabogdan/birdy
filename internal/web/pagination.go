package web

import (
	"net/http"
	"strconv"
)

const defaultPageSize = 50

// parsePageParams reads "offset"/"limit" query params shared by every
// paginated route listing (peer detail tabs, looking glass).
func parsePageParams(r *http.Request) (offset, limit int) {
	offset, _ = strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = defaultPageSize
	}
	return offset, limit
}
