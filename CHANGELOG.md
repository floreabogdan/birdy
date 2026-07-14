# Changelog

All notable changes to birdy are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/floreabogdan/birdy/compare/v0.3.5...HEAD
[0.3.5]: https://github.com/floreabogdan/birdy/compare/v0.3.4...v0.3.5
[0.3.4]: https://github.com/floreabogdan/birdy/compare/v0.3.3...v0.3.4
[0.3.3]: https://github.com/floreabogdan/birdy/compare/v0.3.2...v0.3.3
[0.3.2]: https://github.com/floreabogdan/birdy/compare/v0.3.1...v0.3.2
[0.3.1]: https://github.com/floreabogdan/birdy/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/floreabogdan/birdy/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/floreabogdan/birdy/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/floreabogdan/birdy/releases/tag/v0.1.0
