package store

import (
	"database/sql"
	"fmt"
	"net/netip"
	"strings"
)

// BMPStation is one BGP Monitoring Protocol (RFC 7854) collector BIRD streams to.
//
// BIRD's BMP exporter monitors every BGP session on the router automatically —
// there is no per-peer selection — so a station row is only where the stream
// goes and which RIB views to include. BMP is a "preliminary" feature in BIRD
// (added in 2.14) and the daemon must be built with BMP support; birdy renders
// it, and `bird -p` is what tells you the build supports it.
type BMPStation struct {
	ID          int64
	Name        string
	Description string
	// Address is the monitoring station's IP. BIRD's `station address ip` takes
	// an address literal, not a hostname, so this is validated as one.
	Address string
	Port    int
	Enabled bool
	// PrePolicy mirrors each session's Adj-RIB-In before import filtering;
	// PostPolicy mirrors it after. With neither, BIRD still reports peer up/down
	// and statistics, just no route contents.
	PrePolicy  bool
	PostPolicy bool
	// TxBufferLimit caps pending data (in megabytes) before BIRD drops and
	// restarts the station rather than run the router out of memory. 0 leaves
	// BIRD's default (1024).
	TxBufferLimit int
}

// IsIP reports whether Address parses as an IP literal, the only form BIRD's
// BMP station accepts.
func (b BMPStation) IsIP() bool {
	_, err := netip.ParseAddr(b.Address)
	return err == nil
}

func (b *BMPStation) Validate() map[string]string {
	errs := map[string]string{}

	b.Name = strings.TrimSpace(b.Name)
	if !birdIdent.MatchString(b.Name) {
		errs["name"] = "Use letters, digits and underscore, starting with a letter or underscore (max 63)."
	}
	if strings.ContainsAny(b.Description, "\"\n\r") {
		errs["description"] = "Quotes and line breaks are not allowed."
	}

	b.Address = strings.TrimSpace(b.Address)
	switch {
	case b.Address == "":
		errs["address"] = "Enter the monitoring station's IP address."
	case !b.IsIP():
		errs["address"] = "BIRD's BMP station takes an IP address, not a hostname."
	default:
		// Re-serialise so the rendered config carries a canonical form.
		if addr, err := netip.ParseAddr(b.Address); err == nil {
			b.Address = addr.String()
		}
	}

	if b.Port < 1 || b.Port > 65535 {
		errs["port"] = "Enter a port between 1 and 65535. BMP is 1790 by convention."
	}
	if b.TxBufferLimit < 0 || b.TxBufferLimit > 65535 {
		errs["txBufferLimit"] = "Enter a size in megabytes between 1 and 65535, or 0 to use BIRD's default (1024)."
	}
	return errs
}

const bmpCols = `id, name, description, address, port, enabled, monitor_pre, monitor_post, tx_buffer_limit`

func scanBMP(sc scanner) (BMPStation, error) {
	var b BMPStation
	err := sc.Scan(&b.ID, &b.Name, &b.Description, &b.Address, &b.Port, &b.Enabled,
		&b.PrePolicy, &b.PostPolicy, &b.TxBufferLimit)
	return b, err
}

func (s *Store) ListBMPStations() ([]BMPStation, error) {
	rows, err := s.db.Query(`SELECT ` + bmpCols + ` FROM bmp_stations ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: list BMP stations: %w", err)
	}
	defer rows.Close()
	var out []BMPStation
	for rows.Next() {
		st, err := scanBMP(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Store) GetBMPStationByName(name string) (BMPStation, error) {
	row := s.db.QueryRow(`SELECT `+bmpCols+` FROM bmp_stations WHERE name = ?`, name)
	st, err := scanBMP(row)
	if err == sql.ErrNoRows {
		return BMPStation{}, ErrNotFound
	}
	return st, err
}

func (s *Store) CreateBMPStation(st BMPStation) (int64, error) {
	ts := now()
	res, err := s.db.Exec(`
		INSERT INTO bmp_stations (name, description, address, port, enabled, monitor_pre, monitor_post, tx_buffer_limit, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		st.Name, st.Description, st.Address, st.Port, st.Enabled, st.PrePolicy, st.PostPolicy, st.TxBufferLimit, ts, ts)
	if err != nil {
		return 0, fmt.Errorf("store: create BMP station: %w", err)
	}
	return res.LastInsertId()
}

func (s *Store) UpdateBMPStation(st BMPStation) error {
	res, err := s.db.Exec(`
		UPDATE bmp_stations SET name = ?, description = ?, address = ?, port = ?, enabled = ?,
		                        monitor_pre = ?, monitor_post = ?, tx_buffer_limit = ?, updated_at = ?
		WHERE id = ?`,
		st.Name, st.Description, st.Address, st.Port, st.Enabled, st.PrePolicy, st.PostPolicy, st.TxBufferLimit, now(), st.ID)
	if err != nil {
		return fmt.Errorf("store: update BMP station: %w", err)
	}
	return affectedOne(res)
}

func (s *Store) DeleteBMPStation(id int64) error {
	res, err := s.db.Exec(`DELETE FROM bmp_stations WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete BMP station: %w", err)
	}
	return affectedOne(res)
}
