package store

import (
	"database/sql"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

// How an import policy treats RPKI route-origin validation.
const (
	ROVOff = "off" // no validation at all
	// ROVLog accepts invalid routes but tags them with a large community, so
	// you can count them before you start dropping them.
	ROVLog = "log"
	// ROVReject drops routes whose origin AS contradicts a published ROA.
	ROVReject = "reject"
)

var rovModes = map[string]bool{ROVOff: true, ROVLog: true, ROVReject: true}

// hostname is deliberately narrow: this value is interpolated into bird.conf
// inside a quoted string, and a hostname has no business containing anything
// else. IP literals are accepted separately.
var hostname = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

// RPKIServer is one RTR endpoint feeding BIRD's ROA tables.
type RPKIServer struct {
	ID          int64
	Name        string
	Description string
	Host        string
	Port        int
	Enabled     bool
	// Timers in seconds. 0 means "use BIRD's default".
	Refresh int
	Retry   int
	Expire  int
}

// IsIP reports whether Host is an address literal rather than a name; BIRD
// wants the former unquoted and the latter quoted.
func (s RPKIServer) IsIP() bool {
	_, err := netip.ParseAddr(s.Host)
	return err == nil
}

func (s *RPKIServer) Validate() map[string]string {
	errs := map[string]string{}

	s.Name = strings.TrimSpace(s.Name)
	if !birdIdent.MatchString(s.Name) {
		errs["name"] = "Use letters, digits and underscore, starting with a letter or underscore (max 63)."
	}
	if strings.ContainsAny(s.Description, "\"\n\r") {
		errs["description"] = "Quotes and line breaks are not allowed."
	}

	s.Host = strings.TrimSpace(s.Host)
	switch {
	case s.Host == "":
		errs["host"] = "Enter the RTR server's hostname or IP address."
	case s.IsIP():
		// Re-serialise so the rendered config carries a canonical form.
		if addr, err := netip.ParseAddr(s.Host); err == nil {
			s.Host = addr.String()
		}
	case !hostname.MatchString(s.Host):
		errs["host"] = "Enter a valid hostname (e.g. rtr.rpki.cloudflare.com) or an IP address."
	}

	if s.Port < 1 || s.Port > 65535 {
		errs["port"] = "Enter a port between 1 and 65535. RTR is 323 over TCP; Cloudflare uses 8282."
	}
	for field, v := range map[string]int{"refresh": s.Refresh, "retry": s.Retry, "expire": s.Expire} {
		if v < 0 || v > 172800 {
			errs[field] = "Enter a number of seconds between 1 and 172800, or 0 to use BIRD's default."
		}
	}
	// BIRD requires expire to outlast a refresh cycle; otherwise the ROA table
	// can time out between updates and every route silently becomes unknown.
	if s.Expire > 0 && s.Refresh > 0 && s.Expire <= s.Refresh {
		errs["expire"] = "Expire must be longer than refresh, or the ROA table will time out between updates."
	}
	return errs
}

const rpkiCols = `id, name, description, host, port, enabled, refresh, retry, expire`

func scanRPKI(sc scanner) (RPKIServer, error) {
	var s RPKIServer
	err := sc.Scan(&s.ID, &s.Name, &s.Description, &s.Host, &s.Port, &s.Enabled, &s.Refresh, &s.Retry, &s.Expire)
	return s, err
}

func (s *Store) ListRPKIServers() ([]RPKIServer, error) {
	rows, err := s.db.Query(`SELECT ` + rpkiCols + ` FROM rpki_servers ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: list RPKI servers: %w", err)
	}
	defer rows.Close()
	var out []RPKIServer
	for rows.Next() {
		srv, err := scanRPKI(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, srv)
	}
	return out, rows.Err()
}

func (s *Store) GetRPKIServerByName(name string) (RPKIServer, error) {
	row := s.db.QueryRow(`SELECT `+rpkiCols+` FROM rpki_servers WHERE name = ?`, name)
	srv, err := scanRPKI(row)
	if err == sql.ErrNoRows {
		return RPKIServer{}, ErrNotFound
	}
	return srv, err
}

func (s *Store) CreateRPKIServer(srv RPKIServer) (int64, error) {
	ts := now()
	res, err := s.db.Exec(`
		INSERT INTO rpki_servers (name, description, host, port, enabled, refresh, retry, expire, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		srv.Name, srv.Description, srv.Host, srv.Port, srv.Enabled, srv.Refresh, srv.Retry, srv.Expire, ts, ts)
	if err != nil {
		return 0, fmt.Errorf("store: create RPKI server: %w", err)
	}
	return res.LastInsertId()
}

func (s *Store) UpdateRPKIServer(srv RPKIServer) error {
	res, err := s.db.Exec(`
		UPDATE rpki_servers SET name = ?, description = ?, host = ?, port = ?, enabled = ?,
		                        refresh = ?, retry = ?, expire = ?, updated_at = ?
		WHERE id = ?`,
		srv.Name, srv.Description, srv.Host, srv.Port, srv.Enabled, srv.Refresh, srv.Retry, srv.Expire, now(), srv.ID)
	if err != nil {
		return fmt.Errorf("store: update RPKI server: %w", err)
	}
	return affectedOne(res)
}

func (s *Store) DeleteRPKIServer(id int64) error {
	res, err := s.db.Exec(`DELETE FROM rpki_servers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete RPKI server: %w", err)
	}
	return affectedOne(res)
}
