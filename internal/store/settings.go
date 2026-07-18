package store

import (
	"database/sql"
	"fmt"
	"net/netip"
	"strings"
)

// Settings is the single-row router identity and global configuration: local
// ASN, router ID, the BIRD socket path, the applied-config hash and the raw
// escape-hatch block.
type Settings struct {
	RouterLabel string
	LocalASN    sql.NullInt64
	// RouterID is the BGP router ID written into the rendered config. It is
	// stored rather than read back from BIRD: the config is the source of
	// truth for what BIRD should be, not the other way round.
	RouterID       string
	BirdSocketPath string
	ListenAddr     string
	WebhookURL     string

	// RRClusterID identifies this route reflector to its peers. Optional: BIRD
	// falls back to the router ID, which is right until you run a second
	// reflector for the same clients and need both to share one cluster ID.
	RRClusterID string

	// KernelPrefSrcV4 and KernelPrefSrcV6 pin krt_prefsrc on routes Birdy
	// installs into the kernel FIB. Setting one also opts Birdy-originated static
	// routes for that family into the FIB.
	KernelPrefSrcV4 string
	KernelPrefSrcV6 string
	// KernelExportBGPV4 and KernelExportBGPV6 install only BIRD's selected BGP
	// route for each prefix into the host FIB. They default off.
	KernelExportBGPV4 bool
	KernelExportBGPV6 bool
	UpdateChannel     string

	// RawConfig is appended verbatim to the end of the generated bird.conf.
	// The escape hatch for everything birdy does not model — BFD, extra tables,
	// graceful restart tuning. birdy does not parse it; `bird -p` is the only
	// thing standing between it and a broken router.
	RawConfig string

	// AppliedConfigHash is the sha256 of the bytes birdy last wrote to bird.conf.
	// Empty means birdy has never written it — the authorship guard's cue that
	// the router must be adopted before birdy may overwrite what is there.
	AppliedConfigHash string

	// AccessWhitelist is the IPs/CIDRs allowed to reach birdy at all — an
	// application-level firewall. One per line or comma-separated. Loopback is
	// always allowed and an empty list (or a 0.0.0.0/0 entry) means no
	// restriction, so it defaults open and cannot lock out an SSH tunnel.
	AccessWhitelist string
}

// ValidateRRClusterID accepts an empty value (use the router ID) or an IPv4
// address, which is the only form BIRD takes for a cluster ID.
func (st *Settings) ValidateRRClusterID() string {
	st.RRClusterID = strings.TrimSpace(st.RRClusterID)
	if st.RRClusterID == "" {
		return ""
	}
	addr, err := netip.ParseAddr(st.RRClusterID)
	if err != nil || !addr.Is4() {
		return "Enter an IPv4 address, or leave blank to use the router ID."
	}
	st.RRClusterID = addr.String()
	return ""
}

// ValidateKernelPrefSrcV4 accepts an empty value or an IPv4 address, the only
// form that belongs on the kernel4 protocol. It normalizes the stored value.
func (st *Settings) ValidateKernelPrefSrcV4() string {
	st.KernelPrefSrcV4 = strings.TrimSpace(st.KernelPrefSrcV4)
	if st.KernelPrefSrcV4 == "" {
		return ""
	}
	addr, err := netip.ParseAddr(st.KernelPrefSrcV4)
	if err != nil || !addr.Is4() {
		return "Enter an IPv4 address, or leave blank to let the kernel choose."
	}
	st.KernelPrefSrcV4 = addr.String()
	return ""
}

// ValidateKernelPrefSrcV6 is the IPv6 counterpart, for the kernel6 protocol.
func (st *Settings) ValidateKernelPrefSrcV6() string {
	st.KernelPrefSrcV6 = strings.TrimSpace(st.KernelPrefSrcV6)
	if st.KernelPrefSrcV6 == "" {
		return ""
	}
	addr, err := netip.ParseAddr(st.KernelPrefSrcV6)
	if err != nil || !addr.Is6() || addr.Is4In6() {
		return "Enter an IPv6 address, or leave blank to let the kernel choose."
	}
	st.KernelPrefSrcV6 = addr.String()
	return ""
}

// GetSettings returns the single settings row, or (Settings{}, false, nil) if
// birdy hasn't been initialized yet.
func (s *Store) GetSettings() (Settings, bool, error) {
	var st Settings
	row := s.db.QueryRow(`
		SELECT router_label, local_asn, router_id, bird_socket_path, listen_addr, webhook_url,
		       rr_cluster_id, kernel_prefsrc_v4, kernel_prefsrc_v6,
		       kernel_export_bgp_v4, kernel_export_bgp_v6,
		       update_channel, raw_config, applied_config_hash, access_whitelist
		FROM settings WHERE id = 1`)
	err := row.Scan(&st.RouterLabel, &st.LocalASN, &st.RouterID, &st.BirdSocketPath,
		&st.ListenAddr, &st.WebhookURL, &st.RRClusterID, &st.KernelPrefSrcV4, &st.KernelPrefSrcV6,
		&st.KernelExportBGPV4, &st.KernelExportBGPV6, &st.UpdateChannel,
		&st.RawConfig, &st.AppliedConfigHash,
		&st.AccessWhitelist)
	if err == sql.ErrNoRows {
		return Settings{}, false, nil
	}
	if err != nil {
		return Settings{}, false, fmt.Errorf("store: get settings: %w", err)
	}
	return st, true, nil
}

// SaveSettings upserts the single settings row.
func (s *Store) SaveSettings(st Settings) error {
	ts := now()
	_, err := s.db.Exec(`
		INSERT INTO settings (id, router_label, local_asn, router_id, bird_socket_path, listen_addr,
		                      webhook_url, rr_cluster_id, kernel_prefsrc_v4, kernel_prefsrc_v6,
		                      kernel_export_bgp_v4, kernel_export_bgp_v6, update_channel,
		                      raw_config, created_at, updated_at)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			router_label = excluded.router_label,
			local_asn = excluded.local_asn,
			router_id = excluded.router_id,
			bird_socket_path = excluded.bird_socket_path,
			listen_addr = excluded.listen_addr,
			webhook_url = excluded.webhook_url,
			rr_cluster_id = excluded.rr_cluster_id,
			kernel_prefsrc_v4 = excluded.kernel_prefsrc_v4,
			kernel_prefsrc_v6 = excluded.kernel_prefsrc_v6,
			kernel_export_bgp_v4 = excluded.kernel_export_bgp_v4,
			kernel_export_bgp_v6 = excluded.kernel_export_bgp_v6,
			update_channel = excluded.update_channel,
			raw_config = excluded.raw_config,
			updated_at = excluded.updated_at
	`, st.RouterLabel, st.LocalASN, st.RouterID, st.BirdSocketPath, st.ListenAddr, st.WebhookURL,
		st.RRClusterID, st.KernelPrefSrcV4, st.KernelPrefSrcV6, st.KernelExportBGPV4,
		st.KernelExportBGPV6, normalizedUpdateChannel(st.UpdateChannel), st.RawConfig, ts, ts)
	if err != nil {
		return fmt.Errorf("store: save settings: %w", err)
	}
	return nil
}

func normalizedUpdateChannel(channel string) string {
	if strings.TrimSpace(channel) == "development" {
		return "development"
	}
	return "stable"
}

// SaveUpdateChannel changes only the selected update feed so the updates form
// cannot overwrite router identity or configuration fields.
func (s *Store) SaveUpdateChannel(channel string) error {
	channel = normalizedUpdateChannel(channel)
	ts := now()
	_, err := s.db.Exec(`
		INSERT INTO settings (id, update_channel, created_at, updated_at)
		VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			update_channel = excluded.update_channel,
			updated_at = excluded.updated_at
	`, channel, ts, ts)
	if err != nil {
		return fmt.Errorf("store: save update channel: %w", err)
	}
	return nil
}

// SaveAccessWhitelist updates only the access whitelist, so the settings forms
// cannot clobber each other's fields.
func (s *Store) SaveAccessWhitelist(text string) error {
	res, err := s.db.Exec(`UPDATE settings SET access_whitelist = ?, updated_at = ? WHERE id = 1`, text, now())
	if err != nil {
		return fmt.Errorf("store: save access whitelist: %w", err)
	}
	return affectedOne(res)
}

// SaveRawConfig updates only the escape hatch, so the identity form and the raw
// config form cannot clobber each other's fields.
func (s *Store) SaveRawConfig(raw string) error {
	res, err := s.db.Exec(`UPDATE settings SET raw_config = ?, updated_at = ? WHERE id = 1`, raw, now())
	if err != nil {
		return fmt.Errorf("store: save raw config: %w", err)
	}
	return affectedOne(res)
}
