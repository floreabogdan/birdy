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
- **`/metrics` is unauthenticated** and only served when `--metrics` is passed. Keep it off the
  public internet (birdy binds loopback by default).
- **There is no TLS and no audit log.** birdy is meant to be reached over an SSH tunnel and
  never bound to a public address.

Reports that amount to "you can do damage if you already have the birdy database, the login
cookie, or root on the router" are out of scope — those are equivalent to already controlling
the router.

## Supported versions

Only the latest release (and `main`) is supported. There are no backports.
