package web

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/floreabogdan/birdy/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*.css static/*.js static/fonts/*.woff2
var staticFS embed.FS

var funcs = template.FuncMap{
	"since": func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return time.Since(t).Round(time.Second).String()
	},
	"fmttime": func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return t.Local().Format("2006-01-02 15:04:05")
	},
	// isotime feeds data-ts attributes so the client can render
	// relative times ("2m ago") that stay correct without reloads.
	"isotime": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.UTC().Format(time.RFC3339)
	},
	"eventBadge": func(kind string) template.HTML {
		class, label := "", kind
		switch kind {
		case store.EventSessionUp:
			class, label = "badge-success", "up"
		case store.EventSessionDown:
			class, label = "badge-danger", "down"
		case store.EventFlap:
			class, label = "badge-warning", "flap"
		case store.EventLimitHit:
			class, label = "badge-warning", "limit"
		case store.EventConfigApply:
			class, label = "badge-info", "config"
		case store.EventConfigRevert:
			class, label = "badge-info", "revert"
		case store.EventPrefixDrop:
			class, label = "badge-danger", "drop"
		case store.EventConfigDrift:
			class, label = "badge-warning", "drift"
		case store.EventIRRRefresh:
			class, label = "badge-info", "irr"
		case store.EventBirdUnreach:
			class, label = "badge-danger", "bird down"
		case store.EventBirdReachable:
			class, label = "badge-success", "bird up"
		}
		return template.HTML(`<span class="badge ` + class + `">` + template.HTMLEscapeString(label) + `</span>`)
	},
	"sessionBadge": func(up bool, state string) template.HTML {
		class := "badge-danger"
		if up {
			class = "badge-success"
		}
		return template.HTML(`<span class="badge ` + class + `"><span class="dot"></span>` + template.HTMLEscapeString(state) + `</span>`)
	},
	// versionBadge renders a config version's outcome.
	"versionBadge": func(status string) template.HTML {
		class, label := "badge", status
		switch status {
		case store.ConfigConfirmed:
			class, label = "badge-success", "confirmed"
		case store.ConfigPending:
			class, label = "badge-warning", "pending"
		case store.ConfigReverted:
			class, label = "badge", "reverted"
		case store.ConfigFailed:
			class, label = "badge-danger", "failed"
		}
		return template.HTML(`<span class="badge ` + class + `">` + template.HTMLEscapeString(label) + `</span>`)
	},
	// shortsha abbreviates a config hash for a compact history row.
	"shortsha": func(sha string) string {
		if len(sha) > 12 {
			return sha[:12]
		}
		return sha
	},
	"progressClass": func(pct float64) string {
		switch {
		case pct >= 90:
			return "p-danger"
		case pct >= 70:
			return "p-warning"
		default:
			return ""
		}
	},
	// importLimitPct returns -1 when no numeric limit is configured (e.g. the
	// channel has no import limit set), so the template can hide the bar.
	"importLimitPct": func(imported int, limitStr string) float64 {
		limit, err := strconv.Atoi(strings.TrimSpace(limitStr))
		if err != nil || limit <= 0 {
			return -1
		}
		pct := float64(imported) / float64(limit) * 100
		if pct > 100 {
			pct = 100
		}
		return pct
	},
	// comma groups thousands: a router carries millions of routes and
	// "2600141" is unreadable at a glance. Mirrored by comma() in dashboard.js.
	// Takes ints and the bare numeric strings BIRD hands back (import limits).
	"comma": commaVal,
	// ratio drives the gauge bars on the stat cards. Templates cannot divide.
	"ratio": ratio,
	// has reports membership in an id list, for pre-selecting multi-selects.
	"has": func(ids []int64, id int64) bool { return slices.Contains(ids, id) },
	// fieldErrs collects validation messages whose key shares a prefix, so a
	// form can print every "entry.N" error under the one textarea they came
	// from. Sorted by key so line-3's error never jumps above line-1's.
	"fieldErrs": func(errs map[string]string, keyPrefix string) []string {
		keys := make([]string, 0, len(errs))
		for k := range errs {
			if strings.HasPrefix(k, keyPrefix) {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		out := make([]string, 0, len(keys))
		for _, k := range keys {
			out = append(out, errs[k])
		}
		return out
	},
}

func commaVal(v any) string {
	switch n := v.(type) {
	case int:
		return comma(n)
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return comma(i)
		}
		return n // not a number (e.g. an empty or "off" limit): pass through
	default:
		return fmt.Sprint(v)
	}
}

func comma(n int) string {
	s := strconv.Itoa(n)
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	var b strings.Builder
	for i := range len(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteByte(s[i])
	}
	return sign + b.String()
}

func ratio(part, total int) float64 {
	if total <= 0 {
		return 0
	}
	return min(float64(part)/float64(total)*100, 100)
}

var tmpl = template.Must(template.New("").Funcs(funcs).ParseFS(templateFS, "templates/*.html"))

func render(w http.ResponseWriter, log *slog.Logger, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Error("template render failed", "template", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func staticHandler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	return http.FileServerFS(sub)
}
