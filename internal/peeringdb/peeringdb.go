// Package peeringdb looks a network up in PeeringDB by AS number, so the peer
// form can pre-fill the tedious, error-prone fields: max-prefix limits, a name,
// and the IRR AS-SET. It dials out to a third party, so birdy only uses it when
// the operator opts in.
package peeringdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Net is the slice of a PeeringDB network record birdy cares about.
type Net struct {
	ASN         int64  `json:"asn"`
	Name        string `json:"name"`
	MaxPrefixV4 int    `json:"maxPrefixV4"`
	MaxPrefixV6 int    `json:"maxPrefixV6"`
	IRRASSet    string `json:"irrAsSet"`
}

// Client queries the PeeringDB API. BaseURL is overridable so tests can point it
// at a local server.
type Client struct {
	BaseURL string
	http    *http.Client
}

func New() *Client {
	return &Client{BaseURL: "https://www.peeringdb.com/api", http: &http.Client{Timeout: 12 * time.Second}}
}

// pdbResponse mirrors the fields birdy reads from GET /net?asn=N.
type pdbResponse struct {
	Data []struct {
		ASN           int64  `json:"asn"`
		Name          string `json:"name"`
		InfoPrefixes4 int    `json:"info_prefixes4"`
		InfoPrefixes6 int    `json:"info_prefixes6"`
		IRRASSet      string `json:"irr_as_set"`
	} `json:"data"`
}

// Lookup returns the PeeringDB record for an AS number, or an error if none
// exists. It never blocks longer than the client timeout.
func (c *Client) Lookup(ctx context.Context, asn int64) (Net, error) {
	if asn < 1 || asn > 4294967295 {
		return Net{}, fmt.Errorf("peeringdb: invalid ASN %d", asn)
	}
	u := c.BaseURL + "/net?asn=" + url.QueryEscape(strconv.FormatInt(asn, 10))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Net{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return Net{}, fmt.Errorf("peeringdb: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Net{}, fmt.Errorf("peeringdb: HTTP %d", resp.StatusCode)
	}
	var pr pdbResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return Net{}, fmt.Errorf("peeringdb: decode: %w", err)
	}
	if len(pr.Data) == 0 {
		return Net{}, fmt.Errorf("no PeeringDB record for AS%d", asn)
	}
	n := pr.Data[0]
	return Net{ASN: n.ASN, Name: n.Name, MaxPrefixV4: n.InfoPrefixes4, MaxPrefixV6: n.InfoPrefixes6, IRRASSet: n.IRRASSet}, nil
}
