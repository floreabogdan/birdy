package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// There is no JS toolchain here, so the scripts' HTML-building is checked as
// source. esc() escapes for element text (& < >) but not quotes: a value it
// escapes can close a quoted attribute and add its own — data-href, which
// ui.js navigates to on click, or style. On the dashboard such values come from
// remote birdy instances, so a compromised remote (or anyone on an http://
// federation link) could plant them. Attribute values must use escAttr().
func TestStaticJSAttributeValuesUseAttrEscaper(t *testing.T) {
	attrViaEsc := regexp.MustCompile(`=(?:"[^"'<>]*'|'[^"'<>]*")\s*\+\s*esc\(`)
	scripts, err := fs.Glob(staticFS, "static/*.js")
	if err != nil || len(scripts) == 0 {
		t.Fatalf("no embedded scripts: %v", err)
	}
	for _, name := range scripts {
		src, err := fs.ReadFile(staticFS, name)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if attrViaEsc.MatchString(line) {
				t.Errorf("%s:%d builds an attribute value with esc(); use escAttr():\n\t%s", name, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// The sparkline tooltip shows points parsed back out of data-spark — on the
// dashboard, a remote instance's route history. They are text, never markup.
func TestSparkTooltipDoesNotParseSeriesAsHTML(t *testing.T) {
	src, err := fs.ReadFile(staticFS, "static/spark-hover.js")
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`innerHTML\s*=[^;]*fmt(Count|When)\(`).Match(src) {
		t.Error("spark-hover.js puts series values into innerHTML; set them as textContent")
	}
}
