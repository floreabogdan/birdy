# Changelog

All notable changes to birdy are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **Choose what an iBGP session announces with no export policy.** A plain iBGP
  session with no export chain has always rendered `export all` — right for a full
  mesh, but the peer form described it as "receive-only", which was true only for
  eBGP. The export section now carries an explicit per-peer fallback for iBGP:
  **announce everything (`export all`)** — the full-mesh default, unchanged — or
  **announce nothing (`export none`)**, the same default-deny posture eBGP has
  under RFC 8212, without having to invent a reject-all policy. Existing iBGP
  sessions default to "announce everything", so an upgrade renders byte-for-byte
  as before; the choice only governs the empty-chain fallback and is ignored the
  moment an export policy is attached.

### Changed
- **Theming system, typography and spacing rebuilt — Modern colours preserved.**
  The stylesheet set a sub-1rem font-size on both `html` and `body`, compounding
  to ~12px body text and shrinking every `rem` in the sheet; the root is fixed and
  all type now sits on a modular scale (body is 15px) with line-height and 8pt
  spacing tokens. **Colours, shadows and borders are unchanged** — the Modern look
  is preserved. The browser-local Modern/Original toggle is replaced by a per-user,
  server-side preference under Settings → Theme: a **light / dark / system** mode
  and an **accent colour** — Green (the default Modern palette) plus Ocean, Violet
  and Amber, which recolour only the accents and leave the neutral surfaces alone.
  The preference is stored on your account and follows you across browsers instead
  of living in one browser's localStorage. The Original style is retired; its blue
  is now the Ocean accent. Pages are now full-width, and the settings/detail tab
  bars no longer show a stray scrollbar.
- **The top-bar bell is an unread-alerts counter, not a live down-gauge.** It used
  to show the number of currently-down sessions — which counted a peer you
  *disabled on purpose*, so the badge stuck at "1" and reappeared on every poll.
  The bell now counts the fault events (session down, flap, import-limit hit,
  prefix drop, config drift, BIRD unreachable) you have not seen yet; opening the
  Timeline marks them seen and clears the badge, and the Timeline now accents the
  events that are new since your last visit. A disabled peer records no event, so
  it can never light the bell. "Seen" is tracked per browser; your own config
  applies and model edits, and a session coming back up, do not count.

### Security
- **A blackhole is honoured only for a host route the peer is authorised to
  hold.** With RFC 7999 blackhole acceptance enabled on an import policy, the
  blackhole `accept` was emitted before the allow-list check — so a peer could
  install a discard for *any* `/32` or `/128`, including addresses it does not
  originate (a resolver, a competitor's host). The accept is now gated on coverage
  by the policy's allow-list (each authorised prefix as a supernet), per RFC 7999
  §3.2; an uncovered blackhole falls through to the ordinary allow-list reject.

### Fixed
- **Draining a peer that has an import policy setting local-pref now actually
  deprefers it.** The drain wrote `bgp_local_pref = 0` *before* the import-policy
  call, and any policy that set local-pref (the normal customer/peer/upstream
  pattern) overwrote it — so the session was never deprefered, and traffic that was
  meant to move away before maintenance stayed put. The drain now runs after the
  policy chain, so it is the final word. (The export-side graceful-shutdown signal
  was always correct; only the import direction was defeated.)
- **A blackhole route keeps its origin tag.** The `FROM_CUSTOMER`/`FROM_IX`/
  `FROM_UPSTREAM` tag and per-peer import communities were added *after* the policy
  chain, but a blackhole `accept` inside a policy terminates the filter first — so
  blackhole routes arrived untagged, invisible to every tag-based export policy and
  to the looking glass. Tags are now stamped before the policy chain.
- **A failed per-session detail fetch no longer pages a false "prefix drop".** If
  BIRD's `show protocols all <peer>` timed out for one established session on a busy
  poll, its imported count read as zero and the drop check fired "routes dropped
  from N to 0" for a healthy session. The poller now skips the drop check when the
  detail fetch failed and carries the last known count forward.
- **The live config preview refreshes when you add, remove or reorder a policy.**
  Policy-chain edits change the DOM directly and fire no input/change event, so the
  preview only updated when you next touched another field — most visibly, a
  *removed* policy lingered in the rendered config. The chain now emits a change
  after every structural edit, so the preview (and the readiness checks) always
  reflect the current chain.
- **The peer form no longer calls an iBGP session "receive-only" when it isn't.**
  The export-chain hint was written for eBGP's RFC 8212 default-deny and shown
  verbatim on iBGP peers, where an empty chain actually announces the full table.
  The hint is now role-aware and points at the new fallback choice above.
- **The dashboard's sort, search and filters work immediately.** They used to no-op
  or, when sorting, blank the table to "No BGP sessions are running" for the first
  few seconds, because the live data (and the rows carrying the filter attributes)
  did not arrive until the first poll. The dashboard now fetches once on load.
- **Trend sparkline tooltips survive a poll.** The client-rendered chart put its
  JSON series into a `data-` attribute without escaping the double quote, so after
  the first refresh the attribute truncated and hovering drew nothing. It is now
  attribute-escaped like the server-rendered charts.
- **`/changes` no longer offers "Ready to apply" after `bird -p` has failed.** The
  apply panel checked only that the config rendered, not that the syntax check
  passed, so it showed a green apply button that contradicted its own "Syntax:
  Failed" indicator and that the server would refuse. It now blocks apply until the
  check passes (or is unavailable).
- **A soft apply's filter reload is detached from the browser connection.** Like
  the rest of the armed window, it now uses the request-independent context, so
  losing the browser right after arming no longer cancels the reload that makes the
  filter change visible while the config is still revertible.
- **The dashboard poll ignores a superseded response.** Overlapping polls on a slow
  link could resolve out of order and repaint the table with stale data; the poll
  now aborts the previous request, matching the live preview.

## [0.4.1] - 2026-07-20

### Fixed
- **Original theme: unreadable table headers in light mode.** The light "original"
  palette set the indigo header band but never its text colour, so headers rendered
  as low-contrast grey on blue. They now use the same light header text as the
  original theme's dark variants.

## [0.4.0] - 2026-07-20

### Added
- **Operator-focused navigation and guided workflows.** The modern panel now
  includes collapsible contextual navigation, explicit local/remote router
  context, direct creation actions in the keyboard command palette, guarded
  unsaved editors, peer-type setup profiles, live peer and configuration
  readiness summaries, persistent dashboard columns, and actionable empty
  states. Presets only assist the form; they never choose export policy, save,
  or apply configuration.
- **Neutral modern theme overhaul.** The selectable modern style now uses a
  quieter graphite and soft-white palette, flatter operational surfaces,
  denser navigation and actions, clearer form and table boundaries, and matched
  light, explicit-dark, and system-dark behavior. The original style remains
  unchanged and available.
- **Selectable panel style.** Settings -> Theme now lets operators choose the
  current modern Birdy style or the original owner-facing palette and geometry.
  The choice is browser-local, works with both light and dark modes, and is
  applied before paint to avoid a theme flash.
- **Runtime efficiency improvements.** BIRD identity and disabled-peer lookups
  are cached briefly instead of repeating on every poll, route-history queries
  have a timestamp index, and federation checks share a bounded keep-alive HTTP
  transport.
- **Fleet operations and diagnostics.** Remote instance health is collected in
  the background, state transitions can alert through existing destinations,
  and each instance has a detail page with session state, route totals, model
  coverage, and health status.
- **Exports.** Sessions and activity can be downloaded as bounded CSV or JSON
  from the dashboard and Changes pages.
- **Scoped instance credentials.** Multiple named read-only observation tokens
  can coexist with independent expiry, last-use tracking, and revocation.
- **Fleet labels and responsive controls.** Instance tags are normalized and
  deduplicated, fleet health counters are visible at a glance, and focus,
  keyboard, mobile table, and narrow-screen states are improved.
- **Fleet operations workspace.** Instances can be assigned groups and tags,
  checked before saving, monitored with bounded concurrent health probes, and
  selected from grouped top-bar options with latency and failure state. The
  Instances page also aggregates recent read-only activity from connected
  targets.
- **Safer instance access lifecycle.** Observation tokens support 30-day,
  90-day, one-year, or non-expiring lifetimes and can be revoked immediately.
  Expiry and revocation are enforced server-side, while remote access remains
  limited to dashboard and timeline reads.
- **Dashboard operator tools.** Session rows can be filtered by state, address
  family, and model coverage; compact mode reduces scan density; and a
  keyboard-accessible command palette provides navigation across the panel.
- **Local instance identity.** The local Birdy panel can be given a friendly
  name that is used consistently in selectors, dashboard headings, and fleet
  activity.
- **Stable and development update tracking.** A new System → Updates page shows
  the installed version and commit, checks either the latest published release
  or upstream `main`, and reports when a newer build is available. Checks are
  bounded and cached; installation remains an explicit operator action.
- **Read-only multi-instance dashboard.** Add other Birdy routers under System →
  Instances, select the dashboard target from the top bar, and observe their
  live sessions and route totals through a token-protected dashboard API. Remote
  targets cannot apply configuration or access local management endpoints.
- **Remote dashboard tokens.** Settings → General can generate and rotate a
  high-entropy token for dashboard observation. Only its SHA-256 digest is kept
  on the observed router.

### Security
- **Native HTTPS emits HSTS.** When birdy serves over `--tls-cert`/`--tls-key`
  (or behind a trusted loopback TLS proxy), responses carry a
  `Strict-Transport-Security` header, pinning the browser to HTTPS after the
  first secure visit. It is never sent on plaintext.
- **Remote-instance URLs are validated against their resolved addresses.** A
  hostname that resolves into loopback, the link-local/cloud-metadata range, or
  an unspecified or multicast address is now rejected when the instance is added,
  not only an IP literal — closing an SSRF-by-hostname bypass.
- **Observation tokens are scope-enforced.** Only a dashboard-scoped token
  authorizes the read-only dashboard and timeline API, so a differently-scoped
  token introduced later cannot silently reach those endpoints.
- **Access control and the login lockout key on the real TCP peer**, never a
  spoofable `X-Forwarded-For`. The reverse-proxy implications of that choice are
  documented in `SECURITY.md`.

### Fixed
- **A config apply survives a dropped UI connection.** The arm, confirm, and
  rollback exchanges with BIRD are detached from the HTTP request, so losing the
  browser mid-apply lets BIRD's armed auto-revert govern the outcome instead of
  an interrupted request forcing an immediate rollback with no recorded version.
- **Same-origin write checks accept the correct scheme behind a TLS-terminating
  reverse proxy**, matching the secure-cookie logic, so `fetch`-driven writes are
  no longer rejected in that deployment.
- **Activity exports follow the selected dashboard instance**, matching the
  session export, so the two downloads describe the same router.
- **Remote-instance health refreshes no longer double-fire** up/down alerts when
  a manual refresh overlaps the background poll, and a shutdown mid-refresh no
  longer records a spurious outage.
- **Failed update checks are briefly cached** instead of re-hitting GitHub on
  every System → Updates render.

## [0.3.8] - 2026-07-17

### Added
- **Guarded kernel installation for selected BGP routes.** Settings can now opt
  IPv4 and IPv6 independently into installing BIRD's selected `RTS_BGP` routes in
  the Linux FIB. The option defaults off for fresh installs and is auto-enabled
  for existing routers during upgrade. Birdy-originated static routes (aggregates,
  library statics) are always installed whenever any kernel export is active.
  Birdy never uses a blanket kernel `export all`.
- **Per-session import community tagging.** Peers can add named or literal
  standard and large communities after import policy succeeds, making it easy to
  identify a downstream, IX route server, location, or ingress separately from
  Birdy's automatic relationship tags.
- **Native HTTPS.** `birdy server` accepts paired `--tls-cert` and `--tls-key`
  options, enforces TLS 1.2 or newer, and reports the effective transport
  accurately in the access-control warning.
- **Dashboard model coverage.** The BGP session table now shows configured and
  unmanaged live sessions and reports how much of the running router is
  represented in Birdy's model.

### Changed
- **Full-table polling is responsive.** Session state is published before the
  expensive whole-RIB count, and aggregate route totals refresh once per minute
  instead of blocking every poll.
- **Theme and interaction scripts are shared static assets.** Early theme
  initialization prevents flashes and incomplete light/dark rendering, while
  delegated row navigation and confirmations work without inline JavaScript.
- **Database and backup work is bounded.** SQLite uses a small connection pool,
  snapshot uploads have a hard 64 MiB request limit, and backup downloads stream
  the database into the archive instead of reading another full copy into memory.
- **Risky applies require acknowledgement.** Apply requires an explicit check
  when lint finds dangers or live sessions would disappear from the generated
  model. Missing import limits are surfaced, and a peer without import policy is
  now a danger rather than a low-priority warning.

### Fixed
- **Full-table kernel export cannot capture BGP control-plane routes.** Kernel
  filters reject every imported prefix that covers the router ID, a configured
  peer address, a local session address, or a preferred source. This prevents a
  learned host route from overriding a directly connected peer and prevents a
  covering recursive/unreachable route from blackholing an interface subnet,
  gateway, or BGP session. The default route (0.0.0.0/0, ::/0) is exempt from
  these guards — it covers every address by definition and rejecting it would
  break the common `IMPORT_DEFAULT_ONLY` pattern.

### Upgrade note
- **Existing routers keep installing routes into the kernel.** The migration
  auto-enables the new per-family BGP export switches for databases that already
  have settings, so an upgrade + apply does not silently remove kernel routes.
  Fresh installs default off. Review the new checkboxes under Settings → General
  after upgrading and disable whichever family you do not need.

### Security
- **Kernel synchronization now fails closed on fresh installs.** Generated kernel
  protocols import nothing and export nothing by default. Enabling any kernel
  export admits Birdy-originated static routes automatically; imported BGP routes
  require the separate, explicit per-family setting above.
- **Public HTTP handling is hardened.** The server now has connection timeouts, a
  32 KiB header ceiling, same-origin validation for browser writes, fail-closed
  malformed client handling, and tighter CSP, opener, resource and permissions
  policies.
- **Password changes revoke old sessions atomically.** A successful change
  deletes every existing session for that account and creates one replacement
  session in the same transaction.
- **Sensitive backups are owner-only.** Snapshots, staged restores, restored
  databases and copied database files are written with mode `0600`.
- **Login throttling has bounded memory.** Expired records are pruned and the
  in-memory per-address limiter has a fixed maximum size.
- **Snapshot restore input is bounded before multipart parsing.** Oversized
  requests are rejected before the parser can consume unbounded memory or
  temporary storage.

## [0.3.7] - 2026-07-16

### Added
- **Per-peer interface for link-local BGP sessions.** BIRD requires an `interface`
  directive when the neighbor address is link-local (`fe80::`), because the address
  is ambiguous without knowing which link it sits on. The peer form now has an
  **Interface** field: set it to the name of the network interface (e.g. `eth0`) and
  birdy renders `interface "eth0";` inside the protocol block. Validation enforces
  the field when the neighbor is link-local, and lint surfaces a danger finding for
  the same condition. Optional for all other sessions.

## [0.3.6] - 2026-07-15

### Fixed
- **Apply now actually takes effect — no more `birdc configure` by hand.** Every
  apply defaulted to a BIRD *soft* reconfigure, and soft is defined to leave the
  routes already in the table under the old filters: it applies the new filters
  only to routes that arrive afterward. So a policy or prefix-set change — anything
  that changes which routes you announce or accept — appeared to apply and confirm
  but changed nothing, until you SSH'd in and ran a plain `birdc configure` (a hard
  reconfigure, which restarts the affected protocols and re-runs everything). birdy
  now pairs the soft reconfigure with a `reload`, which re-imports (via route-
  refresh) and re-exports preferred routes, so the change reaches the existing
  table **without bouncing any BGP session**. The reload runs while the config is
  still armed, so its real effect is visible inside the safety window and a bad
  filter still auto-reverts; a rollback or an auto-revert reloads too, so the table
  never lingers on an un-confirmed policy. A peer that lacks the route-refresh
  capability cannot be refreshed without a restart — that is reported after the
  apply rather than failing it, and the config is applied either way. A hard apply
  (Soft reload unchecked) restarts the affected protocols as before and needs no
  reload.

### Added
- **Disable a prefix set without deleting it.** A prefix set now has a power toggle
  on the library list, the way a peer does. A disabled set renders nothing — neither
  its `define` nor, if it originates, its static protocol — so you can stop
  announcing an aggregate and switch it straight back on later, without losing the
  prefixes. The reference cascades: a policy that names a disabled set drops the
  reference instead of emitting a symbol whose define was withheld (which would fail
  `bird -p`). What "drop" means is chosen for safety per direction — an **export**
  policy simply stops announcing that set, while an **import allow-list**
  (*accept only from set*) **fails closed**: it permits nothing rather than silently
  dropping the membership check and accepting the whole table. Lint surfaces both, so
  the change is never silent: a danger for the allow-list that now permits nothing, a
  warning for the announce that was dropped. System sets (the bogon lists) cannot be
  disabled; they are wired into generated filters. The flag is stored so its default
  is "on", so every set that predates the change stays exactly as it rendered before.
- **A preferred source address for kernel routes.** birdy exported its best routes
  into the kernel FIB with a bare `export all`, leaving the kernel to pick the source
  address for the router's own traffic to those destinations — usually the egress
  interface's address, which changes with the path and is rarely the one you announce.
  Settings → General now takes a **Kernel source** address per family: set it and the
  channel renders an `export filter` that stamps `krt_prefsrc` on every route, so
  locally-originated traffic leaves with a stable, chosen source (typically a
  loopback). It is set per family because `kernel4` and `kernel6` are separate
  protocols. Leave a field blank and that channel renders `export all` byte for byte
  as before, so an existing router is untouched across the upgrade.

## [0.3.5] - 2026-07-14

A single change, for the router that is not alone. An internal session could only
ever be `import all; export all;` — fine in a full mesh with one exit, a route
leak and a dead tunnel the moment the far router has an upstream of its own. iBGP
now takes the same policy chains eBGP does, and a session with no chain renders
byte for byte what it rendered before.

### Added
- **iBGP sessions take import and export policies.** They used to be refused —
  `iBGP sessions do not take policies yet` — and every internal session rendered
  `import all; export all;`. That is right for a full mesh where every router shares
  one exit, and wrong the moment a second router has an upstream of its own: it
  inherits a default route it should never have had. Over a **tunnel** that is not
  merely untidy but fatal — the far router installs the default, then tries to reach
  the tunnel's own endpoint through the tunnel, the kernel drops the packets as a
  dead loop, keepalives stop, and the session flaps on the hold timer forever.
  Attaching a chain now renders `ibgp_in_<peer>` / `ibgp_out_<peer>` filters; leaving
  it empty renders exactly what it did before, byte for byte.
  - **An internal import filter never strips your own large communities.** The eBGP
    one deletes them so a peer cannot forge an origin tag; on an internal session
    those same communities *are* the origin tags, stamped at the edge, and every
    downstream export policy reads them. Deleting them would have silently unmade
    every "announce what my customers sent me" decision on the far router.
  - **Enforce-first-AS and origin-peer-only are forced off on iBGP.** Both compare
    the AS path against the peer's ASN, which internally is your own — the first
    would reject every route carrying a path, the second everything you did not
    originate yourself.

## [0.3.4] - 2026-07-13

A release about reading what birdy is telling you. The RPKI dry run now answers the
question it exists for, the charts say *when*, every table has pages, and a session
you switched off stops being reported as a session that failed.

### Added
- **The RPKI dry run says how many routes are invalid.** It listed some of them and
  never gave a number, so you could scroll a page of invalid prefixes and still not
  know whether the total was 12 or 12,000 — which is the only thing the dry run is
  for. BIRD counts them itself (`show route where … count`, one line back, instant
  even on a 1.2M-route table), so the panel now leads with **"742 would be dropped"**,
  broken down per table, and the pager gets real page numbers from the same count.
  ROA tables are excluded — they hold ROAs, not routes, and would have made the
  number nonsense.
- **"Which policies validate" is a table, and lists the policies that *don't*.** It
  was a row of policy-name chips, which could not tell you the thing you actually
  need before switching one to reject: **which peers ride on it**. Now every import
  policy is listed with its RPKI mode, a plain sentence about what it does to an
  invalid route, and the sessions it carries. A policy that validates nothing but
  carries half your peers is exactly what this page should surface, and it used to
  be invisible here.
- **The route-history charts are hoverable.** Point at any spot on a sparkline —
  the peer page's Route history, or the dashboard's Trend column — and it names the
  sample under the cursor: `984,213 routes` and when it was taken, with a guide line
  and a dot on the line itself. The charts used to draw values with the timestamps
  thrown away, so you could see that a session halved but not *when*, which is the
  only question worth asking. Points now carry their time all the way to the
  browser. (These are the only charts in birdy; the stat cards and import-limit bars
  are meters, not series.)
- **Every table is paginated, with numbered pages.** One pager, shared by all of
  them: prev/next, a window of page numbers around the current one, ellipses for
  the gaps, first and last always reachable, and a "rows 201–250 of 1,240" summary.
  Jumping to a page is the thing prev/next cannot do. It draws nothing at all when
  a table fits on one page.
  - **RPKI-invalid routes (dry run) is paginated** — it used to show the first 200
    and stop, with no way to see the rest, which is useless when the whole point is
    counting what a switch to *reject* would drop.
  - The looking glass, peer route tabs, timeline, apply history, peers, policies,
    prefix sets, AS sets, static routes, communities, BMP stations and RTR servers
    all page the same way. The timeline gained real page numbers: it used to walk
    backwards one page at a time on a cursor, so "what happened last Tuesday" meant
    clicking *older* twenty times.
  - Route tables streamed from BIRD (the looking glass, peer routes, RPKI invalids)
    show the pages they can prove exist rather than a page count — birdy will not
    walk a 2.6M-route table just to draw a "of 52,000" in a pager.
- **Disable a session straight from the peers list.** A power button on each row
  switches a peer off (and back on) without opening the form — it is a thing you
  reach for in a hurry. Like every edit in birdy it changes the model, so the row
  says **pending apply** while BIRD still has the session up.
- **"Disabled" is now its own state, not "down".** A peer switched off renders with
  BIRD's `disabled`, so BIRD makes **no connection attempts at all** — but it then
  reports the protocol as plain `down`, exactly like a session that failed. birdy
  now tells the two apart everywhere: the peers list and dashboard show a neutral
  *disabled* badge instead of a red *down*, the health verdict counts it separately
  ("All 2 sessions up · 1 disabled") rather than calling the router unhealthy, and
  **the poller no longer raises down/flap alerts for it** — applying a disable is a
  change you made on purpose, not an outage worth paging you about.
- **The router label is editable.** It named the router in alerts but could only be
  set by `birdy init --label` — Settings showed it as read-only text, so renaming a
  router meant re-initialising it. It is now a field under Settings → General, next
  to the router ID and ASN. (The big name on the dashboard is a different thing: it
  is the system hostname, reported by BIRD.)

### Changed
- **The wide-open warning left the dashboard.** Binding every interface with an
  allow-all access list is still called out — once in the startup log, and on the
  Access settings page that fixes it — but not on the page you keep open all day. A
  warning seen a hundred times is one you stop reading, and it cannot be acted on
  from where it appeared.

## [0.3.3] - 2026-07-13

This release is about the install. birdy used to arrive switched off: every optional
feature behind a flag, write access behind a systemd-unit edit, and a database that
`sudo birdy init` could leave unwritable — which then surfaced as "internal error" at
the first login. **A fresh install now works, with nothing to edit.**

### Added
- **AS sets expand and auto-refresh from the IRR.** The AS-set page recorded the
  source `AS-SET` but never used it — you expanded it yourself and pasted the
  members in. Now it works like a prefix set does: **Expand from IRR** on the form
  fills the member AS numbers in for review, **Auto-refresh from IRR** keeps them
  current on `--irr-refresh-interval`, and **Refresh now** (the ↻ on the list)
  re-expands one set on demand. The list shows each set's AS-SET, its auto badge,
  last sync and any error. Refreshes update the model only — the change waits on
  Changes for you to review and apply, same as everything else. Notes you wrote
  against a member survive the refresh, and an empty expansion — which is what
  `bgpq4` returns for an AS-SET the IRR does not know — keeps the previous members
  rather than emptying a set that would then reject every route.

### Changed
- **Optional features are detected, not declared.** birdy looks for `bgpq4` (IRR
  expansion) and `ping`/`traceroute` (diagnostics) at startup and enables what the
  router actually has, logging the verdict. PeeringDB lookups are on. `--bgpq4 off`,
  `--netdiag=false`, `--peeringdb=false` and `--metrics=false` turn things off; no
  flag turns anything on any more.
- **birdy listens on `0.0.0.0:8080`** (was loopback) and **is no longer `--read-only`
  by default** — the packaged unit ships without the flag, and grants `/etc/bird` to
  the `bird` group, so Adopt and Apply work out of the box. Writing `bird.conf` is
  still a deliberate act in the UI; nothing is written on install. Add `--read-only`
  back to run birdy as a pure viewer.
- **`/metrics` is on by default, but gated on the access list.** It cannot carry a
  session cookie, so serving it from a wide-open bind would publish the router's
  session inventory. It returns 403 while the IP allow-list allows everything, and
  starts serving the moment you narrow the list — no flag, no restart.
- **The dashboard warns when birdy is reachable from any IP** with an allow-all access
  list. Shipping open is only defensible if birdy says so where you will see it.

### Fixed
- **A root-created database no longer bricks the first login.** `birdy init` under
  `sudo` left `birdy.db` owned by root while the service runs as `birdy`: SQLite then
  opened it read-only, birdy started anyway, and logging in — which writes a session
  row — failed with "internal error", the real reason visible only in the journal.
  Now `init` hands the database (and its `-wal`/`-shm`) to the service account, the
  server **refuses to start on a database it cannot write** and prints the `chown` that
  fixes it, `birdy doctor` checks the database *file* and not just its directory, and
  the package's post-install repairs an install that already went wrong.
- **`birdy doctor` no longer warns that "birdy writes as uid 0".** Run under `sudo` it
  judged root, not the `birdy` account the service actually runs as, and cried wolf
  about BIRD being unable to read birdy's files on every correctly-installed router.
- **The "adopt this router" panel explains itself in read-only mode.** It told you to
  adopt and then showed no button, which reads as a broken page rather than a viewer.
- **Editing a prefix set on a birdy without `bgpq4` no longer silently switches its
  auto-refresh off.** The checkbox is not rendered there, so saving the form used to
  clear the opt-in.

## [0.3.2] - 2026-07-13

### Added
- **Looking glass decodes route communities.** With "show all + attributes"
  checked, every route now shows its BGP communities, local-pref, origin and MED.
  Communities are decoded to readable names: your named-communities library, the
  RFC well-known set (`BLACKHOLE`, `NO_EXPORT`, `GRACEFUL_SHUTDOWN`, …), and
  birdy's own origin (`FROM_UPSTREAM`/`FROM_IX`/`FROM_CUSTOMER`) and
  `RPKI_INVALID` tags — each colour-coded by meaning so a blackhole or an
  RPKI-invalid route stands out. Works for the "imported from peer" query too, so
  you can see exactly what a peer is tagging.

### Changed
- **Looking Glass and Diagnostics are now one tabbed page** — Routes (the route
  looking glass) and Diagnostics (ping/traceroute) share a single sidebar entry.
- **Settings is organised into tabs** — General, Bogons, Access, Alerts, Advanced
  — and the alert destinations moved from their own page into the Alerts tab.
- The dashboard's session count and health verdict now count **only BGP sessions**;
  infrastructure protocols (device, kernel, static, RPKI) are shown separately
  rather than inflating the session total.
- **"Routes in RIB"** (and the `birdy_routes_total` metric) **excludes RPKI ROA
  tables**, which otherwise dwarf the real route count once RPKI is running.

### Fixed
- `show protocols` parsing broke on protocol names longer than the fixed column —
  including the aggregate originators birdy generates itself — mis-reading the
  state, cutting the name, and wrongly counting the protocol as down. It now parses
  by whitespace, so long names are safe.
- Infrastructure protocols no longer raise session down/up/flap alerts. An RPKI
  RTR cache reconnecting to a public validator was firing a "flap" every few
  minutes; only BGP sessions raise these alerts now.

## [0.3.1] - 2026-07-12

### Added
- Debian, RPM and Alpine packages built with nfpm, published on each release for
  amd64, arm64 and armhf — install the binary, a systemd unit and a `birdy`
  system user in one step.

## [0.3.0] - 2026-07-12

### Added
- **Audit trail** — the timeline records who applied a config and who changed
  each peer or policy, attributed to the operator who made the change.
- **Named-communities library** — define a community once, give it a readable
  name, and reference it by name in policies and per-peer export.
- **RPKI live-invalids dry-run** — the RPKI page lists the routes BIRD is
  currently tagging invalid, so you can size the impact before switching a policy
  from log-only to drop.
- **Ping and traceroute diagnostics** (opt-in, `--netdiag`) — a reachability
  looking glass from the router itself, alongside the route one.
- Security: response-hardening headers, and `govulncheck` in CI.

### Fixed
- A lint warning when a peer or policy references a community that is not defined.

## [0.2.0] - 2026-07-12

### Added
- **RFC 9234 BGP roles** and a per-user profile page, plus a full `USAGE.md`
  guide to every flag and knob.
- **BMP monitoring stations** (RFC 7854) — stream every session's pre- and
  post-policy RIB to a collector.
- **Seed peers from the running BIRD** — scaffold the model from the sessions
  BIRD already runs, so adopting a router is review-and-import, not re-typing.
- **Config-drift alert** — fires when `bird.conf` changes outside birdy.
- **Route-count history sparklines** on the dashboard and per peer, from samples
  birdy records itself.
- **Scheduled IRR refresh** — keep a prefix set current from its AS-SET (never
  auto-applied).
- Config is written as `bird.conf` plus a `birdy.d/` of per-section includes,
  with a per-file diff browser.
- Pre-deployment safety: doctor read-check, GTSM/graceful-restart, and an
  apply-impact warning.

## [0.1.0] - 2026-07-11

Initial public release. birdy is a single Go binary that runs on a BIRD 2.x
router and gives you:

### Added
- **Observe** — a live dashboard of every BGP session read from BIRD's control
  socket, per-peer detail, a paginated route browser, an on-demand looking glass,
  a flap/limit timeline, and alerts to Slack, Discord, email (SMTP) or a webhook.
- **Model** — peers with roles that drive origin tagging; composable import and
  export policy chains; prefix sets, AS sets and static routes; RPKI ROV; RFC
  7999 RTBH; BFD; AS-path prepending, export communities and one-click drain
  (RFC 8326); PeeringDB auto-fill and IRR expansion with `bgpq4`.
- **Preview and apply** — the whole `bird.conf` rendered from the model, syntax
  checked with `bird -p`, linted for what `bird -p` cannot catch, diffed against
  the running config, and applied with an armed auto-revert and an authorship
  guard. Apply history with re-apply for emergency rollback.
- **Operations** — a Prometheus `/metrics` endpoint, a public `/healthz` probe,
  per-IP login rate-limiting, and a downloadable off-box backup bundle.
- Multi-arch release binaries (Linux amd64/arm64/arm, FreeBSD, macOS) and a
  multi-arch container image on GHCR.

[Unreleased]: https://github.com/floreabogdan/birdy/compare/v0.4.1...HEAD
[0.4.1]: https://github.com/floreabogdan/birdy/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/floreabogdan/birdy/compare/v0.3.8...v0.4.0
[0.3.8]: https://github.com/floreabogdan/birdy/compare/v0.3.7...v0.3.8
[0.3.7]: https://github.com/floreabogdan/birdy/compare/v0.3.6...v0.3.7
[0.3.6]: https://github.com/floreabogdan/birdy/compare/v0.3.5...v0.3.6
[0.3.5]: https://github.com/floreabogdan/birdy/compare/v0.3.4...v0.3.5
[0.3.4]: https://github.com/floreabogdan/birdy/compare/v0.3.3...v0.3.4
[0.3.3]: https://github.com/floreabogdan/birdy/compare/v0.3.2...v0.3.3
[0.3.2]: https://github.com/floreabogdan/birdy/compare/v0.3.1...v0.3.2
[0.3.1]: https://github.com/floreabogdan/birdy/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/floreabogdan/birdy/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/floreabogdan/birdy/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/floreabogdan/birdy/releases/tag/v0.1.0
