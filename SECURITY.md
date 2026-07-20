# Security Policy

birdy is beta software that manages BGP — a router's control plane — and its database
stores **BGP MD5 session passwords in the clear** (BIRD needs them that way). Please treat
a security report here as you would for any infrastructure tool.

## Reporting a vulnerability

**Please report privately. Do not open a public issue for a security problem.**

Use GitHub's private vulnerability reporting: on the repository's **Security** tab, click
**Report a vulnerability** ([Security Advisories](https://github.com/floreabogdan/birdy/security/advisories/new)).
This opens a private channel with the maintainer.

Please include:

- the birdy version (`birdy version`) and the BIRD version,
- what you found and how it can be reproduced,
- the impact you think it has.

## What to expect

This is a personal project maintained on a best-effort basis. There is **no SLA, no bounty,
and no guarantee of a fix or a timeline.** Reports are read and taken seriously, and a fix
will be made when one is warranted and practical. Coordinated disclosure is appreciated: give
a reasonable window before publishing details.

## Scope and non-issues

Some properties are documented and by design, not vulnerabilities:

- **MD5 session passwords are stored in cleartext** in birdy's SQLite database, because BIRD
  requires them in that form. The database file is as sensitive as `bird.conf` itself — protect
  it accordingly.
- **birdy listens on every interface out of the box** (`0.0.0.0:8080`), and its IP allow-list starts
  as allow-all. This is deliberate: a router UI that will not answer until a config file is edited
  does not get set up. birdy warns once in its startup log while it is in that state, and flags it on
  the Access settings page. Narrow it under Settings → Access control — an unlisted address then has its connection closed with no response —
  or bind it closed with `--listen 127.0.0.1:8080` and reach it over an SSH tunnel.
- **`/metrics` is unauthenticated**, because a Prometheus scrape cannot carry a session cookie. It is
  therefore gated on the allow-list: it returns 403 while every IP is allowed, and serves as soon as
  the list is narrowed.
- **There is no TLS by default.** Out of the box birdy serves plaintext HTTP, so on a public address
  the login and session cookie travel in the clear, and the allow-list does nothing about that — it
  governs who may connect, not what is readable on the wire. You can enable native HTTPS by passing a
  certificate and key (`--tls-cert`/`--tls-key`, TLS 1.2+); otherwise put birdy on a management network
  you trust, or on loopback behind an SSH tunnel. Operator actions are recorded in an audit trail on
  the event timeline.
- **The IP allow-list and login lockout key on the real TCP peer**, never a spoofable
  `X-Forwarded-For`. This is correct for the recommended deployments — a directly exposed listener or an
  SSH tunnel. Behind a reverse proxy every request arrives from the proxy (loopback for a local one), so
  the per-IP login lockout and the allow-list see the proxy, not the real client. In that setup, either
  enforce access control and rate-limiting at the proxy, or prefer birdy's own native TLS
  (`--tls-cert`/`--tls-key`) so it is exposed directly and sees real client addresses.

Reports that amount to "you can do damage if you already have the birdy database, the login
cookie, or root on the router" are out of scope — those are equivalent to already controlling
the router.

## Supported versions

Only the latest release (and `main`) is supported. There are no backports.
