package peeringdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLookupParsesNet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "asn=64500") {
			t.Errorf("unexpected query %q", r.URL.RawQuery)
		}
		w.Write([]byte(`{"data":[{"asn":64500,"name":"Example Networks","info_prefixes4":5000,"info_prefixes6":100,"irr_as_set":"AS-EXAMPLE"}]}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, http: srv.Client()}
	n, err := c.Lookup(context.Background(), 64500)
	if err != nil {
		t.Fatal(err)
	}
	if n.Name != "Example Networks" || n.MaxPrefixV4 != 5000 || n.MaxPrefixV6 != 100 || n.IRRASSet != "AS-EXAMPLE" {
		t.Fatalf("parsed wrong: %+v", n)
	}
}

func TestLookupNoRecord(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, http: srv.Client()}
	if _, err := c.Lookup(context.Background(), 64500); err == nil {
		t.Error("an empty result should be an error")
	}
}

func TestLookupRejectsBadASN(t *testing.T) {
	c := New()
	if _, err := c.Lookup(context.Background(), 0); err == nil {
		t.Error("ASN 0 should be rejected without a request")
	}
}
