package store

const schema = `
CREATE TABLE IF NOT EXISTS settings (
	id               INTEGER PRIMARY KEY CHECK (id = 1),
	router_label     TEXT NOT NULL DEFAULT '',
	local_asn        INTEGER,
	router_id        TEXT NOT NULL DEFAULT '',
	bird_socket_path TEXT NOT NULL DEFAULT '/run/bird/bird.ctl',
	listen_addr      TEXT NOT NULL DEFAULT '127.0.0.1:8080',
	webhook_url      TEXT NOT NULL DEFAULT '',
	created_at       TEXT NOT NULL,
	updated_at       TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	username      TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	created_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
	token      TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS events (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	ts         TEXT NOT NULL,
	kind       TEXT NOT NULL,
	protocol   TEXT NOT NULL DEFAULT '',
	message    TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_events_ts ON events(ts DESC);

-- Periodic per-session route-count samples, so the dashboard can draw a small
-- history sparkline without a Prometheus/Grafana stack. Written on a slow
-- cadence (not every poll) and pruned to a retention window, so the table stays
-- tiny even on a router carrying a full table.
CREATE TABLE IF NOT EXISTS route_samples (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	ts       TEXT NOT NULL,
	protocol TEXT NOT NULL,
	imported INTEGER NOT NULL,
	exported INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_samples_proto_ts ON route_samples(protocol, ts);

-- One peers row renders one "protocol bgp" block. The address family of a
-- session is derived from neighbor_ip rather than stored: a BGP session to an
-- IPv6 neighbor carries an ipv6 channel, and splitting v4/v6 into separate
-- protocols is how BIRD configs are written in practice.
-- role decides the large-community tag birdy stamps on routes learned from this
-- peer, which is how export policies later say "announce what my customers and
-- IX peers sent me" without knowing their prefixes in advance.
CREATE TABLE IF NOT EXISTS peers (
	id                   INTEGER PRIMARY KEY AUTOINCREMENT,
	name                 TEXT NOT NULL UNIQUE,
	description          TEXT NOT NULL DEFAULT '',
	role                 TEXT NOT NULL DEFAULT 'upstream',
	enabled              INTEGER NOT NULL DEFAULT 1,
	neighbor_ip          TEXT NOT NULL,
	remote_asn           INTEGER NOT NULL,
	local_ip             TEXT NOT NULL DEFAULT '',
	multihop             INTEGER NOT NULL DEFAULT 0,
	passive              INTEGER NOT NULL DEFAULT 0,
	password             TEXT NOT NULL DEFAULT '',
	import_limit         INTEGER NOT NULL DEFAULT 0,
	import_limit_action  TEXT NOT NULL DEFAULT 'restart',
	-- Off for IXP route servers: they do not prepend themselves, so the first
	-- AS in the path is the peer behind them, not the session's own ASN.
	enforce_first_as     INTEGER NOT NULL DEFAULT 1,
	created_at           TEXT NOT NULL,
	updated_at           TEXT NOT NULL
);

-- One policies table with a direction discriminator. Import policies only ever
-- reject; export policies only ever accept. That is what lets a peer chain
-- several of them: imports compose with AND, exports with OR.
CREATE TABLE IF NOT EXISTS policies (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	name        TEXT NOT NULL UNIQUE,
	description TEXT NOT NULL DEFAULT '',
	direction   TEXT NOT NULL,
	builtin     INTEGER NOT NULL DEFAULT 0,

	-- import-only knobs
	default_route         TEXT NOT NULL DEFAULT 'reject',  -- reject | accept | only
	min_len_v4            INTEGER NOT NULL DEFAULT 0,
	max_len_v4            INTEGER NOT NULL DEFAULT 0,
	min_len_v6            INTEGER NOT NULL DEFAULT 0,
	max_len_v6            INTEGER NOT NULL DEFAULT 0,
	reject_own_asn        INTEGER NOT NULL DEFAULT 1,
	max_as_path_len       INTEGER NOT NULL DEFAULT 0,
	bogon_asns            TEXT NOT NULL DEFAULT 'all',     -- off | all | except_private
	accept_only_set_id    INTEGER REFERENCES prefix_sets(id) ON DELETE RESTRICT,
	set_local_pref        INTEGER NOT NULL DEFAULT 0,

	-- export-only knobs
	announce_everything    INTEGER NOT NULL DEFAULT 0,
	announce_default       INTEGER NOT NULL DEFAULT 0,
	announce_from_upstream INTEGER NOT NULL DEFAULT 0,
	announce_from_ix       INTEGER NOT NULL DEFAULT 0,
	announce_from_customer INTEGER NOT NULL DEFAULT 0,

	-- used by both directions
	reject_bogon_prefixes INTEGER NOT NULL DEFAULT 1,

	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

-- Export policies announce the prefixes in these sets.
CREATE TABLE IF NOT EXISTS policy_prefix_sets (
	policy_id INTEGER NOT NULL REFERENCES policies(id) ON DELETE CASCADE,
	set_id    INTEGER NOT NULL REFERENCES prefix_sets(id) ON DELETE RESTRICT,
	position  INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (policy_id, set_id)
);

-- The ordered chain. Position is per (peer, direction), derived from the
-- policy's own direction.
CREATE TABLE IF NOT EXISTS peer_policies (
	peer_id   INTEGER NOT NULL REFERENCES peers(id) ON DELETE CASCADE,
	policy_id INTEGER NOT NULL REFERENCES policies(id) ON DELETE RESTRICT,
	position  INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (peer_id, policy_id)
);

CREATE TABLE IF NOT EXISTS prefix_sets (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	name        TEXT NOT NULL UNIQUE,
	description TEXT NOT NULL DEFAULT '',
	family      TEXT NOT NULL DEFAULT 'ipv4',
	-- originate: render a static protocol announcing these prefixes as
	-- blackhole routes. You must originate what you announce.
	originate   INTEGER NOT NULL DEFAULT 0,
	builtin     INTEGER NOT NULL DEFAULT 0,
	-- system sets (the bogon lists) are referenced by name from generated
	-- filters, so they cannot be renamed, re-familied or deleted. They are
	-- edited under Settings and never offered in a prefix-set picker.
	system      INTEGER NOT NULL DEFAULT 0,
	created_at  TEXT NOT NULL,
	updated_at  TEXT NOT NULL
);

-- A named list of AS numbers, used to decide which origins a peer may announce.
--
-- This is where an IRR AS-SET lands. BIRD has no concept of an AS-SET: the
-- operator (or, later, bgpq4) expands AS-CUSTOMER into its member ASNs and
-- stores them here. The source column records the AS-SET it was expanded from,
-- so that expansion can be automated later without guessing.
CREATE TABLE IF NOT EXISTS as_sets (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	name        TEXT NOT NULL UNIQUE,
	description TEXT NOT NULL DEFAULT '',
	source      TEXT NOT NULL DEFAULT '',
	created_at  TEXT NOT NULL,
	updated_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS as_set_entries (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	set_id   INTEGER NOT NULL REFERENCES as_sets(id) ON DELETE CASCADE,
	asn_low  INTEGER NOT NULL,
	asn_high INTEGER NOT NULL,
	note     TEXT NOT NULL DEFAULT '',
	position INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_ase_set ON as_set_entries(set_id, position);

-- RTR servers feeding BIRD the ROA table used for origin validation.
--
-- Running a local validator (Routinator, StayRTR, rpki-client) is the usual
-- production answer; the public endpoints are fine to start with. Timers of 0
-- mean "leave BIRD's default alone".
CREATE TABLE IF NOT EXISTS rpki_servers (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	name        TEXT NOT NULL UNIQUE,
	description TEXT NOT NULL DEFAULT '',
	host        TEXT NOT NULL,
	port        INTEGER NOT NULL DEFAULT 323,
	enabled     INTEGER NOT NULL DEFAULT 1,
	refresh     INTEGER NOT NULL DEFAULT 0,
	retry       INTEGER NOT NULL DEFAULT 0,
	expire      INTEGER NOT NULL DEFAULT 0,
	created_at  TEXT NOT NULL,
	updated_at  TEXT NOT NULL
);

-- BMP (RFC 7854) monitoring stations BIRD streams every session's state to.
-- BIRD's exporter watches all BGP sessions automatically; a row is only where
-- the stream goes and which RIB views (pre- and/or post-import-filter) to send.
CREATE TABLE IF NOT EXISTS bmp_stations (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	name            TEXT NOT NULL UNIQUE,
	description     TEXT NOT NULL DEFAULT '',
	address         TEXT NOT NULL,
	port            INTEGER NOT NULL DEFAULT 1790,
	enabled         INTEGER NOT NULL DEFAULT 1,
	monitor_pre     INTEGER NOT NULL DEFAULT 1,
	monitor_post    INTEGER NOT NULL DEFAULT 1,
	-- Megabytes of pending data before BIRD restarts the station. 0 = default.
	tx_buffer_limit INTEGER NOT NULL DEFAULT 0,
	created_at      TEXT NOT NULL,
	updated_at      TEXT NOT NULL
);

-- AS numbers that must never appear in a received AS path. Data, not code:
-- IANA hands out new ranges, and a peer may legitimately use a private ASN.
CREATE TABLE IF NOT EXISTS bogon_asns (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	asn_low    INTEGER NOT NULL,
	asn_high   INTEGER NOT NULL,
	-- private ranges are excluded by a policy set to "all except private".
	is_private INTEGER NOT NULL DEFAULT 0,
	note       TEXT NOT NULL DEFAULT '',
	position   INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS prefix_set_entries (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	set_id   INTEGER NOT NULL REFERENCES prefix_sets(id) ON DELETE CASCADE,
	prefix   TEXT NOT NULL,
	-- BIRD prefix pattern suffix: "", "+", "-" or "{low,high}".
	modifier TEXT NOT NULL DEFAULT '',
	position INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_pse_set ON prefix_set_entries(set_id, position);

-- Reachability no protocol discovers on its own: a subnet behind something that
-- does not speak BGP, or a route to a far router's loopback so an iBGP session
-- peering on loopbacks can resolve its next hop.
--
-- One route per prefix. Two routes to the same net in one static protocol is
-- never what anyone means, and BIRD will not tell you which one won.
CREATE TABLE IF NOT EXISTS static_routes (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	prefix      TEXT NOT NULL UNIQUE,
	-- via | blackhole | unreachable | prohibit
	action      TEXT NOT NULL DEFAULT 'blackhole',
	next_hop    TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	enabled     INTEGER NOT NULL DEFAULT 1,
	created_at  TEXT NOT NULL,
	updated_at  TEXT NOT NULL
);

-- One row per apply. The full rendered config is kept (the database already
-- holds the session passwords it contains) so a version can be re-shown, and so
-- a confirmed apply can rewrite the file BIRD is now running from memory.
CREATE TABLE IF NOT EXISTS config_versions (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	created_at       TEXT NOT NULL,
	sha256           TEXT NOT NULL,
	size             INTEGER NOT NULL,
	config_text      TEXT NOT NULL,
	-- On-disk backup of whatever the config was before this apply overwrote it:
	-- a snapshot directory (bird.conf + birdy.d/) for split layouts, or a single
	-- .bak file for older single-file backups.
	backup_path      TEXT NOT NULL DEFAULT '',
	-- pending | confirmed | reverted | failed
	status           TEXT NOT NULL DEFAULT 'pending',
	-- When the armed auto-revert fires, RFC3339. Empty once resolved.
	timeout_deadline TEXT NOT NULL DEFAULT '',
	message          TEXT NOT NULL DEFAULT '',
	resolved_at      TEXT NOT NULL DEFAULT '',
	-- The exact multi-file layout this apply wrote, as JSON (a render.FileSet), so
	-- confirm and re-apply can rewrite the whole set. Empty for older single-file
	-- versions, which fall back to writing config_text to bird.conf.
	config_files     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_cv_status ON config_versions(status);

-- Where session alerts are delivered. One row per destination, so an operator
-- can page one channel and email another. The type discriminator decides which
-- fields matter: url for the webhook kinds, the smtp_* fields for email.
CREATE TABLE IF NOT EXISTS alert_destinations (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	name          TEXT NOT NULL UNIQUE,
	-- webhook | slack | discord | email
	type          TEXT NOT NULL,
	enabled       INTEGER NOT NULL DEFAULT 1,
	url           TEXT NOT NULL DEFAULT '',
	smtp_host     TEXT NOT NULL DEFAULT '',
	smtp_port     INTEGER NOT NULL DEFAULT 587,
	smtp_username TEXT NOT NULL DEFAULT '',
	smtp_password TEXT NOT NULL DEFAULT '',
	smtp_from     TEXT NOT NULL DEFAULT '',
	smtp_to       TEXT NOT NULL DEFAULT '',
	-- none | starttls | tls
	smtp_security TEXT NOT NULL DEFAULT 'starttls',
	-- comma-separated event kinds this destination wants; empty means all.
	events        TEXT NOT NULL DEFAULT '',
	created_at    TEXT NOT NULL,
	updated_at    TEXT NOT NULL
);
`
