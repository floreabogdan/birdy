package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// schemaVersion is the migration level this build expects. Bump it and add a
// case to migrate() when the shape of an existing database has to change.
const schemaVersion = 31

// migrate brings an existing database up to schemaVersion. The CREATE TABLE
// statements in schema.go are all IF NOT EXISTS and run unconditionally, so
// migrations here only handle what that cannot express: new columns on tables
// that already exist, and one-time data seeding.
//
// birdy's database is a single file the user can snapshot and restore, so
// migrations must be forward-only and safe to re-run.
func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	if version >= schemaVersion {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if version < 1 {
		// Databases created before M2 have a settings table with no router_id.
		if err := ensureColumn(tx, "settings", "router_id", `ALTER TABLE settings ADD COLUMN router_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
		if err := seedStarterPack(tx); err != nil {
			return err
		}
	}

	if version < 2 {
		// Peers grew a role (which drives community tagging) and an
		// enforce-first-AS switch; "kind" and the single export prefix set are
		// subsumed by roles and export policies respectively.
		if err := ensureColumn(tx, "peers", "role", `ALTER TABLE peers ADD COLUMN role TEXT NOT NULL DEFAULT 'upstream'`); err != nil {
			return err
		}
		if err := ensureColumn(tx, "peers", "enforce_first_as", `ALTER TABLE peers ADD COLUMN enforce_first_as INTEGER NOT NULL DEFAULT 1`); err != nil {
			return err
		}
		if has, err := hasColumn(tx, "peers", "kind"); err != nil {
			return err
		} else if has {
			if _, err := tx.Exec(`UPDATE peers SET role = 'ibgp' WHERE kind = 'ibgp'`); err != nil {
				return err
			}
			if _, err := tx.Exec(`ALTER TABLE peers DROP COLUMN kind`); err != nil {
				return fmt.Errorf("drop peers.kind: %w", err)
			}
		}
		// The old peers.export_set_id is replaced by export policies. Nothing
		// migrates it: a peer that announced a set now needs an export policy
		// naming that set, which is a decision, not a mechanical rewrite.
		if has, err := hasColumn(tx, "peers", "export_set_id"); err != nil {
			return err
		} else if has {
			if _, err := tx.Exec(`ALTER TABLE peers DROP COLUMN export_set_id`); err != nil {
				return fmt.Errorf("drop peers.export_set_id: %w", err)
			}
		}
		if err := seedPolicies(tx); err != nil {
			return err
		}
	}

	if version < 3 {
		// The bogon lists move out of the Library and into Settings: they are
		// referenced by name from every generated filter, so they must not be
		// renamed, deleted, or picked from a dropdown as if they were a normal
		// set. The ASN list stops being hardcoded in the renderer.
		if err := ensureColumn(tx, "prefix_sets", "system", `ALTER TABLE prefix_sets ADD COLUMN system INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE prefix_sets SET system = 1, originate = 0 WHERE name IN (?, ?)`, BogonSetV4, BogonSetV6); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM bogon_asns`).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if err := replaceBogonASNs(tx, DefaultBogonASNs()); err != nil {
				return err
			}
		}
		if err := seedPolicies(tx); err != nil { // adds EXPORT_OWN
			return err
		}
	}

	if version < 4 {
		// Origin-AS filtering: "accept only what this peer originates" (per
		// peer, since it needs the peer's own ASN) and "accept only origins in
		// this AS set" (per policy, so it can be shared).
		if err := ensureColumn(tx, "peers", "origin_peer_only", `ALTER TABLE peers ADD COLUMN origin_peer_only INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
		if err := ensureColumn(tx, "policies", "origin_as_set_id", `ALTER TABLE policies ADD COLUMN origin_as_set_id INTEGER REFERENCES as_sets(id)`); err != nil {
			return err
		}
	}

	if version < 5 {
		// RPKI origin validation: off, log-only, or reject invalids.
		if err := ensureColumn(tx, "policies", "rov", `ALTER TABLE policies ADD COLUMN rov TEXT NOT NULL DEFAULT 'off'`); err != nil {
			return err
		}
	}

	if version < 6 {
		// A fresh install should have somewhere to put its own aggregates, and
		// EXPORT_OWN should already point at it.
		if err := seedAnnounceSets(tx); err != nil {
			return err
		}
	}

	if version < 7 {
		// A public RTR endpoint, disabled, so RPKI is one click away without
		// birdy ever dialling out on its own.
		if err := seedRPKIServers(tx); err != nil {
			return err
		}
	}

	if version < 8 {
		// iBGP was rendered as "import all; export all;" and nothing else, which
		// readvertises eBGP routes carrying the original next hop. Unless the IGP
		// happens to carry every peering subnet, the receiving router cannot
		// resolve it and the traffic is black-holed. Default the fix ON: an
		// operator who wants BIRD's stock behaviour has to ask for it.
		if err := ensureColumn(tx, "peers", "next_hop_self", `ALTER TABLE peers ADD COLUMN next_hop_self INTEGER NOT NULL DEFAULT 1`); err != nil {
			return err
		}
		if err := ensureColumn(tx, "peers", "rr_client", `ALTER TABLE peers ADD COLUMN rr_client INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
		if err := ensureColumn(tx, "settings", "rr_cluster_id", `ALTER TABLE settings ADD COLUMN rr_cluster_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
		// The escape hatch: config birdy does not model, appended verbatim.
		if err := ensureColumn(tx, "settings", "raw_config", `ALTER TABLE settings ADD COLUMN raw_config TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}

	if version < 9 {
		// An originated aggregate is an anchor route, and what it does with the
		// traffic it attracts is a choice: drop it silently (blackhole) or say so
		// (unreachable / prohibit). It used to always be blackhole.
		if err := ensureColumn(tx, "prefix_sets", "originate_action", `ALTER TABLE prefix_sets ADD COLUMN originate_action TEXT NOT NULL DEFAULT 'blackhole'`); err != nil {
			return err
		}
	}

	if version < 10 {
		// The authorship guard. This is the sha256 of the exact bytes birdy last
		// wrote to bird.conf and that BIRD is running. Empty means birdy has
		// never written the file, so it must not overwrite a config it did not
		// author without the operator explicitly adopting the router first.
		if err := ensureColumn(tx, "settings", "applied_config_hash", `ALTER TABLE settings ADD COLUMN applied_config_hash TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}

	if version < 11 {
		// eBGP export transforms: prepend our AS to steer inbound traffic, attach
		// communities to signal upstreams, and a drain flag for RFC 8326 graceful
		// shutdown before maintenance.
		if err := ensureColumn(tx, "peers", "prepend_count", `ALTER TABLE peers ADD COLUMN prepend_count INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
		if err := ensureColumn(tx, "peers", "export_communities", `ALTER TABLE peers ADD COLUMN export_communities TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
		if err := ensureColumn(tx, "peers", "drained", `ALTER TABLE peers ADD COLUMN drained INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}

	if version < 12 {
		// A policy can match a single community: import rejects a route carrying
		// it, export accepts one — the customer-signalling pattern.
		if err := ensureColumn(tx, "policies", "match_community", `ALTER TABLE policies ADD COLUMN match_community TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}

	if version < 13 {
		// Alerts moved from a single settings.webhook_url to a table of
		// destinations. Carry an existing webhook over so nobody loses it.
		var legacy string
		if err := tx.QueryRow(`SELECT webhook_url FROM settings WHERE id = 1`).Scan(&legacy); err != nil && err != sql.ErrNoRows {
			return err
		}
		if strings.TrimSpace(legacy) != "" {
			ts := now()
			if _, err := tx.Exec(`
				INSERT INTO alert_destinations (name, type, enabled, url, smtp_port, smtp_security, created_at, updated_at)
				VALUES ('webhook', 'webhook', 1, ?, 587, 'starttls', ?, ?)`, legacy, ts, ts); err != nil {
				return err
			}
		}
	}

	if version < 14 {
		// A destination can now choose which event kinds it wants; empty = all.
		if err := ensureColumn(tx, "alert_destinations", "events", `ALTER TABLE alert_destinations ADD COLUMN events TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}

	if version < 15 {
		// BFD per peer, and RFC 7999 blackhole acceptance per import policy.
		if err := ensureColumn(tx, "peers", "bfd", `ALTER TABLE peers ADD COLUMN bfd INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
		if err := ensureColumn(tx, "policies", "accept_blackhole", `ALTER TABLE policies ADD COLUMN accept_blackhole INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}

	if version < 17 {
		// A prefix set can be expanded from an IRR AS-SET with bgpq4.
		if err := ensureColumn(tx, "prefix_sets", "source", `ALTER TABLE prefix_sets ADD COLUMN source TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}

	if version < 16 {
		// Which BGP sessions were established when a config was applied, so the
		// pending panel can flag any that regressed.
		if err := ensureColumn(tx, "config_versions", "baseline_sessions", `ALTER TABLE config_versions ADD COLUMN baseline_sessions TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}

	if version < 18 {
		// RFC 9234 BGP roles / Only-To-Customer. Off by default: turning it on for
		// an existing peer can reset the session if the far end has a conflicting
		// role, so it stays a deliberate per-peer opt-in.
		if err := ensureColumn(tx, "peers", "bgp_role", `ALTER TABLE peers ADD COLUMN bgp_role INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}

	if version < 19 {
		// The rendered config is now written as bird.conf plus a birdy.d/ of
		// per-section includes. A pending apply records the exact file set so
		// confirm and re-apply write what was armed, not a fresh render.
		if err := ensureColumn(tx, "config_versions", "config_files", `ALTER TABLE config_versions ADD COLUMN config_files TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}

	if version < 20 {
		// Per-peer GTSM (RFC 5082) and graceful-restart negotiation. GTSM defaults
		// off; graceful restart defaults to "aware", which is BIRD's own default.
		if err := ensureColumn(tx, "peers", "gtsm", `ALTER TABLE peers ADD COLUMN gtsm INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
		if err := ensureColumn(tx, "peers", "graceful_restart", `ALTER TABLE peers ADD COLUMN graceful_restart TEXT NOT NULL DEFAULT 'aware'`); err != nil {
			return err
		}
	}

	if version < 21 {
		// A prefix set expanded from an IRR AS-SET can be kept current on a timer.
		// auto_refresh opts a set in; last_refreshed / refresh_error record the
		// most recent successful sync and the last failure, for the form to show.
		if err := ensureColumn(tx, "prefix_sets", "auto_refresh", `ALTER TABLE prefix_sets ADD COLUMN auto_refresh INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
		if err := ensureColumn(tx, "prefix_sets", "last_refreshed", `ALTER TABLE prefix_sets ADD COLUMN last_refreshed TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
		if err := ensureColumn(tx, "prefix_sets", "refresh_error", `ALTER TABLE prefix_sets ADD COLUMN refresh_error TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}

	if version < 22 {
		// The audit trail: an event can now record the operator who caused it (a
		// config apply, a model edit). System/BIRD events leave it empty.
		if err := ensureColumn(tx, "events", "actor", `ALTER TABLE events ADD COLUMN actor TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}

	if version < 23 {
		// The named-communities library. Seed the well-known ones so it is not
		// empty on an existing database.
		if err := seedCommunities(tx); err != nil {
			return err
		}
	}

	if version < 24 {
		// The access whitelist — an application-level IP allow-list. Defaults to
		// 0.0.0.0/0 (allow all) so an upgrade never locks anyone out.
		if err := ensureColumn(tx, "settings", "access_whitelist", `ALTER TABLE settings ADD COLUMN access_whitelist TEXT NOT NULL DEFAULT '0.0.0.0/0'`); err != nil {
			return err
		}
	}

	if version < 25 {
		// An AS set can now be expanded from its IRR AS-SET with bgpq4, on demand
		// or on a timer — the same deal prefix sets got in v17/v21. Until now
		// source was only a note to self.
		if err := ensureColumn(tx, "as_sets", "auto_refresh", `ALTER TABLE as_sets ADD COLUMN auto_refresh INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
		if err := ensureColumn(tx, "as_sets", "last_refreshed", `ALTER TABLE as_sets ADD COLUMN last_refreshed TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
		if err := ensureColumn(tx, "as_sets", "refresh_error", `ALTER TABLE as_sets ADD COLUMN refresh_error TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}

	if version < 26 {
		// Pin the preferred source address (krt_prefsrc) on routes birdy exports
		// to the kernel FIB — the address the kernel stamps as the source of
		// locally-originated traffic to those destinations, typically a loopback.
		// One per family; empty (the default) leaves kernel export disabled.
		if err := ensureColumn(tx, "settings", "kernel_prefsrc_v4", `ALTER TABLE settings ADD COLUMN kernel_prefsrc_v4 TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
		if err := ensureColumn(tx, "settings", "kernel_prefsrc_v6", `ALTER TABLE settings ADD COLUMN kernel_prefsrc_v6 TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}

	if version < 27 {
		// A prefix set can be switched off without deleting it — its define and any
		// originator stop rendering, like disabling a peer. Stored as `disabled`
		// rather than `enabled` so the column's zero value is "on", which is what
		// every set that predates this migration must stay.
		if err := ensureColumn(tx, "prefix_sets", "disabled", `ALTER TABLE prefix_sets ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}

	if version < 28 {
		// Link-local IPv6 addresses require BIRD to know which interface the
		// neighbor sits on — without it BIRD rejects the config with "Link-local
		// addresses require defined interface".
		if err := ensureColumn(tx, "peers", "interface", `ALTER TABLE peers ADD COLUMN interface TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}

	if version < 29 {
		// Per-neighbor inbound tagging: operators can identify routes learned
		// from a route server, downstream, or other specific session using
		// standard or large communities from the named community library.
		if err := ensureColumn(tx, "peers", "import_communities", `ALTER TABLE peers ADD COLUMN import_communities TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}

	if version < 30 {
		// Installing selected BGP routes into the host FIB is deliberately
		// opt-in per family. Existing routers stay fail-closed across upgrade.
		if err := ensureColumn(tx, "settings", "kernel_export_bgp_v4", `ALTER TABLE settings ADD COLUMN kernel_export_bgp_v4 INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
		if err := ensureColumn(tx, "settings", "kernel_export_bgp_v6", `ALTER TABLE settings ADD COLUMN kernel_export_bgp_v6 INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
		// Existing routers that had `export all` (the old default) were
		// installing every route into the kernel FIB. Switching to `export
		// none` silently on upgrade would remove their kernel routes on the
		// next apply. Preserve the old behaviour for databases that already
		// have settings: enable both families so the first apply after
		// upgrading is not a surprise.
		var hasSettings int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM settings WHERE id = 1`).Scan(&hasSettings); err != nil {
			return err
		}
		if hasSettings > 0 {
			if _, err := tx.Exec(`UPDATE settings SET kernel_export_bgp_v4 = 1, kernel_export_bgp_v6 = 1 WHERE id = 1`); err != nil {
				return err
			}
		}
	}

	if version < 31 {
		// Update checks track either signed stable releases or the main
		// development branch. Stable is the conservative upgrade default.
		if err := ensureColumn(tx, "settings", "update_channel", `ALTER TABLE settings ADD COLUMN update_channel TEXT NOT NULL DEFAULT 'stable'`); err != nil {
			return err
		}
	}

	// PRAGMA does not accept bind parameters.
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion)); err != nil {
		return fmt.Errorf("set user_version: %w", err)
	}
	return tx.Commit()
}

func hasColumn(tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return false, fmt.Errorf("table_info(%s): %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, ctype      string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func ensureColumn(tx *sql.Tx, table, column, ddl string) error {
	has, err := hasColumn(tx, table, column)
	if err != nil || has {
		return err
	}
	if _, err := tx.Exec(ddl); err != nil {
		return fmt.Errorf("add column %s.%s: %w", table, column, err)
	}
	return nil
}
