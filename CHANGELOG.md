# Changelog

All notable changes to birdy are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **Looking glass decodes route communities.** With "show all + attributes"
  checked, every route now shows its BGP communities, local-pref, origin and MED.
  Communities are decoded to readable names: your named-communities library, the
  RFC well-known set (`BLACKHOLE`, `NO_EXPORT`, `GRACEFUL_SHUTDOWN`, …), and
  birdy's own origin (`FROM_UPSTREAM`/`FROM_IX`/`FROM_CUSTOMER`) and
  `RPKI_INVALID` tags — each colour-coded by meaning so a blackhole or an
  RPKI-invalid route stands out. Works for the "imported from peer" query too, so
  you can see exactly what a peer is tagging.

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

[Unreleased]: https://github.com/floreabogdan/birdy/compare/v0.3.1...HEAD
[0.3.1]: https://github.com/floreabogdan/birdy/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/floreabogdan/birdy/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/floreabogdan/birdy/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/floreabogdan/birdy/releases/tag/v0.1.0
