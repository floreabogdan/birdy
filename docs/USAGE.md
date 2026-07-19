# birdy — usage & configuration guide

birdy is a single Go binary you run **on a BIRD 2.x router**. It reads BIRD's
control socket to show you what every BGP session is doing, and it keeps a
database model of your intended config (peers, policies, prefix/AS sets) that it
renders into a complete `bird.conf` and — when you let it — applies with an armed
auto-revert.

This guide covers installing the dependencies, installing birdy, first run, every
command-line flag, and what each knob on every screen does.

> [!CAUTION]
> birdy renders the **entire** `bird.conf` from its own database. It does not
> merge with or preserve a hand-written config. Read the warnings in the
> [README](../README.md#read-this-before-you-install-it) before pointing it at a
> router you care about.

All addresses and AS numbers in this guide are from the documentation ranges of
[RFC 5398](https://www.rfc-editor.org/rfc/rfc5398) (AS64496–64511, AS65536–65551),
[RFC 5737](https://www.rfc-editor.org/rfc/rfc5737) (192.0.2.0/24, 198.51.100.0/24,
203.0.113.0/24) and [RFC 3849](https://www.rfc-editor.org/rfc/rfc3849) (2001:db8::/32).

---

## Contents

1. [Requirements](#1-requirements)
2. [Installing the dependencies](#2-installing-the-dependencies)
3. [Installing birdy](#3-installing-birdy)
4. [First run](#4-first-run)
5. [Command & flag reference](#5-command--flag-reference)
6. [How birdy works: model → preview → apply](#6-how-birdy-works-model--preview--apply)
7. [Peer parameters](#7-peer-parameters)
8. [Policy parameters](#8-policy-parameters)
9. [Library: prefix sets, AS sets, static routes](#9-library-prefix-sets-as-sets-static-routes)
10. [RPKI](#10-rpki)
11. [BMP monitoring](#11-bmp-monitoring)
12. [Alerts](#12-alerts)
13. [Settings](#13-settings)
14. [Your profile](#14-your-profile)
15. [Security](#15-security)

---

## 1. Requirements

birdy runs **on the router**, in the same host as BIRD, because it talks to BIRD
over a local Unix control socket. It is not a controller for a fleet.

**Runtime (on the router):**

- **BIRD 2.x**, running, with its control socket reachable. Tested against
  BIRD 2.17.1. The RFC 9234 role feature needs a BIRD new enough to support BGP
  roles (2.0.8 or later); everything else works on any 2.x.
- The **`bird` binary** on `PATH` (or point `--bird-binary` at it). birdy shells
  out to `bird -p` to syntax-check a candidate config before it is ever applied.
  This is the same binary as the daemon — installing BIRD gives you both.
- Linux or FreeBSD. A 64-bit or 32-bit CPU; the release ships `linux/amd64`,
  `linux/arm64`, `linux/arm`, `freebsd/amd64`, and macOS builds.

**Optional runtime extras:**

- **bgpq4** — one-click expansion of an IRR `AS-SET` into a prefix set (the prefixes)
  or an AS set (the origin ASNs), and scheduled auto-refresh of either. Used
  automatically when installed; nothing to enable.
- **A local RPKI validator** speaking RTR (Routinator, StayRTR, rpki-client) — for
  origin validation. You can start with a public RTR endpoint instead.
- **Outbound HTTPS** — for PeeringDB lookups (on by default; `--peeringdb=false` to disable) or
  email alerts (SMTP).

**Build (only if you compile birdy yourself):**

- **Go 1.25+**. The binary is fully static: `CGO_ENABLED=0`, and SQLite is the
  pure-Go [modernc.org/sqlite](https://modernc.org/sqlite), so there is nothing to
  link against and nothing to install at runtime.

---

## 2. Installing the dependencies

### BIRD 2.x

Debian / Ubuntu:

```sh
sudo apt update
sudo apt install bird2
```

The daemon runs as user `bird`, group `bird`, and its control socket is usually
`/run/bird/bird.ctl`. Confirm it is up:

```sh
systemctl status bird
sudo birdc show status          # birdc is BIRD's own client
```

### bgpq4 (optional — IRR expansion)

```sh
sudo apt install bgpq4
```

That is all: birdy finds `bgpq4` on `PATH` at startup and enables IRR expansion.
Without it, prefix sets and AS sets still work; you just fill them in by hand
instead of expanding an `AS-SET`.

### An RPKI validator (optional)

Run a validator locally and point an RTR server entry at it in the birdy UI, or
begin with a public endpoint. birdy never dials RTR itself — it configures BIRD to.

### Go (only to build from source)

Install Go 1.25+ from [go.dev/dl](https://go.dev/dl/). Not needed if you download a
prebuilt binary or use Docker.

---

## 3. Installing birdy

Pick one. All three put a single binary on the router.

### Option A — download a release binary

Grab the archive for your platform from the
[latest release](https://github.com/floreabogdan/birdy/releases/latest), verify it
against `SHA256SUMS.txt`, and install it:

```sh
tar -xzf birdy_*_linux_amd64.tar.gz
sudo install birdy /usr/local/bin/birdy
birdy version
```

### Option B — `go install`

```sh
go install github.com/floreabogdan/birdy/cmd/birdy@latest
sudo install "$(go env GOPATH)/bin/birdy" /usr/local/bin/birdy
```

Or cross-compile from your workstation and copy one file to the router:

```sh
COMMIT=$(git rev-parse HEAD)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -ldflags="-s -w -X github.com/floreabogdan/birdy/internal/buildinfo.Commit=$COMMIT" \
  -o birdy ./cmd/birdy
scp birdy user@router:/tmp/birdy
ssh user@router 'sudo install /tmp/birdy /usr/local/bin/birdy'
```

### Option C — Docker

A multi-arch image is published to the GitHub Container Registry. Share BIRD's
control socket into the container and publish the UI only to the host loopback.
See [`docker-compose.yml`](../docker-compose.yml) and the README's Docker section
for the full setup.

### A user to run it as

birdy needs to **read BIRD's control socket**, which is owned by group `bird`. Run
it as an unprivileged user in that group — not as root. The sample unit in
[`deploy/birdy.service`](../deploy/birdy.service) does exactly this:

```ini
[Service]
User=birdy
Group=bird
ExecStart=/usr/local/bin/birdy server --db /var/lib/birdy/birdy.db
ProtectSystem=strict
ReadWritePaths=/var/lib/birdy /etc/bird
```

No feature flags: birdy detects what the router has (`bgpq4`, `ping`, `traceroute`)
and enables what is there. `/etc/bird` is in `ReadWritePaths` because
`ProtectSystem=strict` makes the filesystem read-only for the unit, so an apply
would fail even when the file permissions allow it. To run birdy as a pure viewer
instead, add `--read-only` to `ExecStart`.

Create the user and state directory:

```sh
sudo useradd --system --gid bird --home-dir /var/lib/birdy --shell /usr/sbin/nologin birdy
sudo install -d -o birdy -g bird /var/lib/birdy
```

For **apply** to work, the `birdy` user also needs write access to the `/etc/bird`
directory itself (birdy writes atomically: a temp file, then a rename). The
packages do this for you — `chgrp bird /etc/bird && chmod g+w /etc/bird`.

---

## 4. First run

**1. Create the database and admin account** (`birdy init`, run once):

```sh
sudo -u birdy birdy init \
  --db /var/lib/birdy/birdy.db \
  --asn 64496 \
  --router-id 192.0.2.1 \
  --label edge1
```

You will be prompted for an admin password (minimum 8 characters). You can also
pass `--password`, but a flag can land in your shell history, so the prompt is
preferred. The ASN and router ID are the two values every rendered config needs;
both can be changed later under **Settings**.

**2. Preflight** (`birdy doctor`) — checks it can reach BIRD, read the socket, find
`bird -p`, and reach the config paths:

```sh
sudo birdy doctor
```

It checks the control socket, `bird -p`, that `--bird-conf` is the file BIRD really
loads, that the **database file** is writable by the service account, and that BIRD
can read the files birdy writes.

**3. Run the server.** For a quick look, run it directly:

```sh
sudo -u birdy birdy server
```

For production, install the systemd unit (the packages already did):

```sh
sudo cp deploy/birdy.service /etc/systemd/system/birdy.service
sudo systemctl daemon-reload
sudo systemctl enable --now birdy
```

**4. Open the UI.** birdy listens on **port 8080 on every interface**, so it is
reachable the moment it starts: `http://<router>:8080`.

**Then narrow who can reach it.** birdy has no TLS, and its IP allow-list starts as
allow-all (it says so in the startup log, and flags it on the page below). Under
**Settings → Access control**, list the addresses allowed to reach birdy; every other connection is
closed with no response (this also switches on the unauthenticated `/metrics`).
Prefer it closed? Run it on loopback and tunnel in:

```sh
birdy server --listen 127.0.0.1:8080
ssh -L 8080:127.0.0.1:8080 user@router   # then browse to http://localhost:8080
```

Log in, and under **Settings → Router identity** confirm the router ID and local
ASN. birdy can write `bird.conf`, but only when you press **Adopt** and then
**Apply** — nothing is written on install. Add `--read-only` to the unit if you want
it to stay a viewer for good.

---

## 5. Command & flag reference

birdy has four subcommands: `init`, `doctor`, `server`, `version`. Run
`birdy <command> -h` on the box for the built-in help.

### `birdy init`

Creates the database and the admin user. Fails if an account already exists.

| Flag | Default | What it does |
|------|---------|--------------|
| `--db` | `/var/lib/birdy/birdy.db` | Path to birdy's SQLite database. |
| `--socket` | `/run/bird/bird.ctl` | BIRD control socket path, stored in settings. |
| `--listen` | `0.0.0.0:8080` | Address the web UI listens on, stored in settings. Use `127.0.0.1:8080` to keep it closed (SSH tunnel). |
| `--label` | *(empty)* | Friendly name for this router (e.g. its hostname). |
| `--asn` | `0` | Local AS number. Used by the config renderer; settable later. |
| `--router-id` | *(empty)* | BGP router ID, written as an IPv4 address. Settable later. |
| `--username` | `admin` | Admin username. |
| `--password` | *(prompt)* | Admin password. Omit it to be prompted (preferred). Minimum 8 characters. |

### `birdy doctor`

Runs preflight checks and exits non-zero if any hard check fails. Read-only. The
checks cover the BIRD binary, the control socket, the config directory,
apply-readiness (including that `--bird-conf` is the file BIRD actually loads),
and — before you drop `--read-only` — that **BIRD can read what birdy writes**:
the split layout writes `0640` files in a `0750 birdy.d/`, so if birdy is neither
BIRD's user nor in BIRD's group, the first apply produces a config BIRD cannot
read. Run `doctor` as the user birdy will run as to make this check meaningful.

| Flag | Default | What it does |
|------|---------|--------------|
| `--socket` | `/run/bird/bird.ctl` | BIRD control socket to test. |
| `--config-dir` | `/etc/bird` | BIRD config directory to check for reachability. |
| `--bird-conf` | `/etc/bird/bird.conf` | The `bird.conf` birdy reads and (unless read-only) writes. |
| `--bird-binary` | `bird` | `bird` binary name or path, for `bird -p`. |
| `--systemd-unit` | `bird` | systemd unit BIRD runs under. |
| `--db` | `/var/lib/birdy/birdy.db` | birdy's database. |

### `birdy server`

Runs the web UI and the background poller.

| Flag | Default | What it does |
|------|---------|--------------|
| `--db` | `/var/lib/birdy/birdy.db` | Path to birdy's SQLite database. |
| `--socket` | *(from init)* | Override the BIRD control socket path. |
| `--listen` | *(from init)* | Override the listen address. |
| `--tls-cert` | *(empty)* | PEM certificate for native HTTPS. Must be used with `--tls-key`. |
| `--tls-key` | *(empty)* | PEM private key for native HTTPS. Must be used with `--tls-cert`. |
| `--read-only` | `false` | **Run as a pure viewer** — never issue a write command to BIRD and never write `bird.conf`. Note it does not stop birdy writing its **own** database: logins, events and history are written in any mode. |
| `--bird-conf` | `/etc/bird/bird.conf` | The running BIRD config birdy reads and (unless read-only) writes. **Must be the same path BIRD was started with** (`bird -c`) for apply to work. |
| `--bird-backup-dir` | `/var/lib/birdy/bird-backups` | Where a copy of `bird.conf` is saved before each apply overwrites it. |
| `--bird-binary` | `bird` | `bird` executable used for `bird -p` config checks. |
| `--apply-timeout` | `60` | Seconds an applied config has to be confirmed before BIRD auto-reverts it. |
| `--poll-interval` | `4s` | How often to poll BIRD for session state. |
| `--snapshot-dir` | `/var/lib/birdy/snapshots` | Directory for nightly database snapshots. |
| `--snapshot-interval` | `24h` | How often to take a nightly snapshot. |
| `--snapshot-retain` | `14` | How many nightly snapshots to keep. |
| `--connect-timeout` | `30s` | How long to retry connecting to BIRD at startup. |
| `--alert-cooldown` | `5m` | Suppress a repeat alert for the same session within this window (`0` disables). |
| `--prefix-drop-ratio` | `0.5` | Alert when a session's imported routes fall to this fraction of the previous poll (`0` disables). |
| `--metrics` | `true` | Serve the Prometheus `/metrics` endpoint. It is **unauthenticated**, so it returns 403 until the access list is narrowed from allow-all. `--metrics=false` disables it entirely. |
| `--peeringdb` | `true` | PeeringDB lookups on the peer form (dials out to peeringdb.com). `--peeringdb=false` disables it. |
| `--bgpq4` | `auto` | IRR `AS-SET` expansion on prefix sets and AS sets. `auto` uses `bgpq4` when it is installed; `off` disables it; or give an explicit path. |
| `--netdiag` | `true` | The **Diagnostics** page: ping/traceroute from the router, enabled when those tools are installed. A read-only operation, safe in read-only mode. `--netdiag=false` disables it. |
| `--drift-check-interval` | `30s` | How often to check whether `bird.conf` changed outside birdy, alerting if it did (`0` disables). Inert until birdy owns a config. |
| `--sample-interval` | `1m` | How often to record a per-session route-count point for the dashboard history sparklines (`0` disables). |
| `--sample-retain` | `168h` | How long to keep route-count history samples. |
| `--irr-refresh-interval` | `24h` | How often to re-expand auto-refresh prefix sets and AS sets from IRR via `bgpq4` (`0` disables; requires `--bgpq4`). |

### `birdy version`

Prints the build version and exits.

---

## 6. How birdy works: model → preview → apply

birdy keeps two separate things:

- **What BIRD is actually doing** — read live from the control socket. This is the
  dashboard, per-peer detail, the looking glass, and the timeline. Available in
  read-only mode with nothing else configured.
- **What you want BIRD to do** — a database model of peers, policies and sets.
  birdy renders this model into a complete BIRD config (a `bird.conf` that
  includes a `birdy.d/` of per-section files — see **Split layout** below).

Editing the model changes nothing on the router. When you visit **Changes**, birdy:

1. renders the candidate `bird.conf` from the model,
2. syntax-checks it with `bird -p`,
3. shows a unified **diff** against the running config,
4. runs a **linter** for things `bird -p` cannot catch — route leaks, a session
   that would accept nothing, unreachable filter branches, an RTR server nobody
   validates against,
5. flags any **live BGP session the model does not include** — because birdy
   renders the whole config, applying would tear those sessions down. This is the
   guardrail for pointing birdy at a router whose sessions you have not modelled
   yet: add each as a peer of the same name first, or expect it to be removed.

If birdy is **not** read-only, you can then **Apply**: it snapshots the current
config, writes the new one, runs `configure check`, then `configure timeout`. BIRD
holds the new config with an **armed auto-revert** — confirm within the window
(`--apply-timeout`) to keep it, or do nothing and BIRD rolls back on its own.

A **soft** reload (the default) keeps BGP sessions up. This matters more than it
sounds: BIRD's soft reconfigure leaves the routes already in the table under the
*old* filters — it only applies the new ones to routes that arrive afterward — so
on its own a soft apply would silently do nothing to a policy or prefix change.
birdy therefore follows the soft reconfigure with a `reload`, which re-imports
(via route-refresh) and re-exports preferred routes so the change reaches the
existing table without a session bounce. The reload runs while the config is still
armed, so its effect is visible in the safety window and a bad filter still
auto-reverts. A peer that does not support route-refresh cannot be refreshed
without a restart; birdy notes that after applying rather than failing the apply.
Uncheck **soft** to restart the affected protocols instead — a harder apply that
bounces sessions but needs no route-refresh support.

**Split layout.** birdy does not write one giant `bird.conf`. It writes a small
`bird.conf` that `include`s one file per section from a `birdy.d/` directory
beside it — `00-header.conf`, `03-sets-prefixes.conf`, one `09-peers-*.conf` per
peer, and so on. On a large router this keeps each file small and reviewable, and
the **Changes** diff becomes a browsable tree showing exactly which file each
change lands in. birdy owns `birdy.d/` exclusively and rewrites it in full on
every apply. What BIRD loads — every include spliced together — is identical to
the single-file config it replaces, so the split is invisible to the diff, the
syntax check, and the authorship hash.

**Authorship guard.** birdy stores a hash of the config it last wrote — computed
from the included files as BIRD would splice them, so an edit to any `birdy.d/`
file is detected — and refuses to overwrite a config it did not author. A
hand-managed file must be explicitly **adopted** first (which backs it up). An
existing single-file birdy install stays owned across the upgrade; its first apply
lays down the split layout. This is how it avoids clobbering a config you wrote by
hand. Beyond the guard, a background check (`--drift-check-interval`) **alerts**
when the on-disk config diverges from what birdy applied — a hand edit, a `birdc`
reconfigure, or a revert birdy did not perform — so you learn about drift without
opening the Changes page. It is inert until birdy owns a config, so a read-only
viewer never false-alarms.

**Adopting a router with live sessions.** Because birdy renders the *whole* config
from its model, a session the model does not name would be torn down on the first
apply — the Changes page flags exactly these as "would remove". To start from what
is already running rather than a blank model, use **Peers → Import from BIRD**
(`/peers/seed`): it reads every live BGP session off the control socket and
proposes an editable peer for each, guessing the role (iBGP when the neighbor AS
equals your own, otherwise upstream — review it) and never enabling RFC 9234 role
negotiation, so importing cannot reset a live session. It only writes birdy's
model, so it works in read-only mode. Import, review the diff until "would remove"
is empty, then apply.

**Route history.** The dashboard grid and each peer's detail page draw route-count
history charts from samples birdy records itself on `--sample-interval` (pruned to
`--sample-retain`) — enough to see when a session started leaking or losing prefixes
without a Prometheus/Grafana stack. **Hover a chart** and it names the point under
the cursor: the route count and the moment it was sampled. "It halved" is only half
an answer; *when* it halved is what you correlate against a flap or an apply.

**Pagination.** Every table pages the same way: prev/next, numbered pages with
ellipses, first and last always reachable, and a `rows 201–250 of 1,240` summary. It
draws nothing when a table fits on one page. Tables streamed from BIRD (the looking
glass, the per-peer route browsers, the RPKI invalids) offer the pages they can prove
exist rather than a page count — birdy will not walk a multi-million-route table just
to number a pager.

**Backups.** Each apply (and each adopt) snapshots the whole config — `bird.conf`
plus `birdy.d/` — into a timestamped directory under the backup path, and a
rejected or reverted apply restores it exactly.

**Apply history.** Every applied config is kept — browse it, diff any version
against what is running, and re-apply an old one as an emergency rollback.

---

## 7. Peer parameters

A peer is one BGP session and renders one `protocol bgp` block. Its address family
(IPv4 vs IPv6 channel) is derived from the neighbor address. Fields marked *eBGP*
or *iBGP* apply only to that kind of session; the form hides the ones that do not
apply to the role you pick.

| Parameter | Applies to | What it does |
|-----------|-----------|--------------|
| **Name** | all | The BIRD protocol name. Letters, digits, underscore; starts with a letter or underscore. |
| **Description** | all | Free text, rendered as the protocol's `description`. |
| **Enabled** | all | Off renders BIRD's `disabled`, so BIRD makes **no connection attempts at all**. Toggle it straight from the peers list (the power button) — like every edit it changes the model, so the row reads *pending apply* until you apply. A disabled peer shows as **disabled**, not *down*, and raises no down/flap alerts: switching it off is a decision, not an outage. |
| **Role** | all | The relationship: **upstream** (sells you transit), **IX peer** (settlement-free), **customer** (buys transit from you), or **iBGP** (inside your AS). Routes learned from a peer are tagged with its role via a large community, so export policies can say "announce what my customers sent me" without knowing their prefixes. iBGP also switches the session to internal. |
| **Enabled** | all | Unchecked renders `disabled;` — the session is configured but not brought up. |
| **Neighbor address** | all | The peer's IP. Its family decides whether the session carries an ipv4 or ipv6 channel. |
| **Local address** | all | Your side of the session. Blank lets BIRD choose. |
| **Remote AS** | all | The peer's AS number (1–4294967295; reserved numbers are rejected). |
| **Multihop TTL** | all | `0` for a directly connected peer; otherwise the TTL for a multihop session (e.g. an upstream a couple of hops away). |
| **Passive** | all | Wait for the peer to open the connection instead of initiating. |
| **MD5 password** | all | TCP-MD5 session password. Stored so BIRD can use it, masked everywhere in the UI. Blank on the edit form means "unchanged". |
| **Import limit / action** | all | Cap on accepted prefixes, and what BIRD does when it is hit: warn, block further routes, restart, or disable. `0` = no limit. |
| **RFC 9234 role** | eBGP | Negotiate a BGP role and use the Only-To-Customer (OTC) attribute so BIRD drops route leaks in the protocol itself. birdy derives the role from the one above: you are a `customer` of an upstream, a `provider` to a customer, a lateral `peer` at an IX. **On by default for new peers.** Enabling it on an existing session can briefly reset it if the far end has a conflicting role configured. |
| **Require first AS** | eBGP | Reject a route whose first AS-path entry is not the peer's AS. Turn this **off for an IXP route server**, which forwards routes without prepending itself. |
| **Origin peer only** | eBGP | Accept a prefix only if the peer *originated* it — transit for them, but not for their downstreams. Their own prepending still works. To carry their downstreams instead, leave this off and point an import policy at an AS set. |
| **Import communities** | eBGP | Communities added after a route passes this peer's import policy. Use a named library community or a literal standard/large value to identify a specific downstream, IX route server, location, or ingress. Birdy's broader relationship tag is added separately. |
| **AS-path prepend** | eBGP | Prepend your own AS this many times (0–10) to everything you announce here. A longer path is less preferred, steering inbound traffic **away** from this peer. |
| **Export communities** | eBGP | Communities attached to every route you announce here — e.g. an upstream's "do not export to your other peers" signal. One per line, standard (`ASN:value`) or large (`ASN:x:y`). |
| **Drain** | eBGP | Signal RFC 8326 graceful shutdown to the peer and deprefer its routes, so traffic moves off the session before you take it down for maintenance. Does **not** disable the session. |
| **Next-hop-self** | iBGP | Readvertise routes with your own address as the next hop. On by default: without it, an eBGP route carries the *external* peer's address, which the far end of the iBGP session usually cannot reach. Leave it on unless your IGP carries the peering subnets. |
| **Route reflector client** | iBGP | Reflect iBGP routes to this peer, lifting the rule that stops readvertising iBGP routes to other iBGP peers. Set a cluster ID under Settings if you run more than one reflector. |
| **Policy chains** | all | An **iBGP** session with no chains carries everything in both directions (`import all; export all;`) — the conventional full-mesh config. That includes any default route the far router learned from *its* upstream, which is a trap when the session runs over a **tunnel**: the far end installs your default, then tries to reach the tunnel's own endpoint *through the tunnel*, and the tunnel dies (the kernel calls this a dead loop and counts it under `collisions`). Attach chains and the internal session filters like any other — e.g. an import policy that rejects the default and accepts only your internal prefix set. Unlike eBGP, an internal import filter **never strips your own large communities**: on that session they are the origin tags the route was stamped with at the edge, and every export policy downstream reads them. (The other half of the tunnel fix lives in the OS, not in BIRD: pin the tunnel endpoint with a host route through the underlay, `ip route add <peer>/32 via <underlay-gw>`.) |
| **BFD** | all | Bidirectional Forwarding Detection — tear the session down within a second of a link failure instead of waiting out the hold timer. Needs a BFD-capable path. |
| **GTSM** | eBGP | Generalized TTL Security Mechanism (RFC 5082): send with a maximal TTL and drop received packets whose TTL is lower than expected, so an off-path attacker cannot spoof the session. For a multihop peer, set **Multihop TTL** correctly so BIRD computes the right expected TTL. |
| **Graceful restart** | all | Negotiate BGP graceful restart so forwarding continues across a control-plane restart on either end: **aware** (help a restarting neighbour — BIRD's default), **on** (negotiate in both directions), or **off** (drop routes immediately). |
| **Import / export policy chains** | eBGP | Ordered lists of policies. **Imports compose with AND** (a route must survive every import policy); **exports compose with OR** (a route is announced if any export policy permits it). With no export policy the session is receive-only (RFC 8212 default-deny). |

**Clone a peer** to use one as a template: birdy copies the role, policy chains,
limits and transforms, and drops only the identity (name, addresses, ASN) and the
password.

---

## 8. Policy parameters

A policy is a reusable filter fragment with a **direction**. Import policies only
ever *reject*; export policies only ever *accept*. That asymmetry is what lets a
peer chain several of them (imports AND-compose, exports OR-compose). A policy can
be attached to many peers.

**Import policies:**

| Parameter | What it does |
|-----------|--------------|
| **Default route** | How to treat a default (`0.0.0.0/0` / `::/0`): reject it, accept it, or accept *only* it. |
| **Prefix length bounds** | Minimum and maximum accepted prefix length, per family — the classic "no longer than /24, no shorter than /8" sanity filter. |
| **Reject own ASN** | Drop a route whose AS-path contains your own AS (a loop or a leak coming back at you). |
| **Max AS-path length** | Reject absurdly long paths. |
| **Bogon ASNs** | Reject paths containing bogon AS numbers: off, all of them, or all except private ranges (so you can peer with someone who legitimately uses a private ASN). The list itself is editable under Settings. |
| **RPKI ROV** | Route Origin Validation against the RTR/ROA table: off, log-only, or drop invalids. |
| **Accept only from set** | Accept only prefixes contained in a chosen prefix set — a tight allow-list. |
| **Set local preference** | Rewrite `bgp_local_pref` on accepted routes to steer your outbound path selection. |
| **Reject bogon prefixes** | Drop routes for bogon address space (also available on exports). Uses the editable bogon prefix lists in Settings. |
| **Match community** | Reject a route carrying a specific community — the "customer signals don't-accept" pattern. |
| **Accept blackhole** | Accept RFC 7999 blackhole (`/32` or `/128`) routes from this peer, for customer-triggered RTBH. |

**Export policies:**

| Parameter | What it does |
|-----------|--------------|
| **Announce toggles** | Announce everything, the default route, or routes learned from upstreams / IX peers / customers — selected by the role tags birdy stamps on import. |
| **Announce these sets** | Announce the prefixes in one or more prefix sets (your own aggregates and their originated blackhole anchors). |
| **Match community** | Accept (announce) a route carrying a specific community — the "customer signals please-announce-this" pattern. |
| **Reject bogon prefixes** | Never announce bogon space, regardless of what else matches. |

birdy ships a **starter pack** of sane import/export policies so a fresh install is
not a blank page.

---

## 9. Library: prefix sets, AS sets, static routes

- **Prefix sets** — named lists of prefixes (with BIRD pattern suffixes like `+`,
  `-`, `{low,high}`). Used as allow-lists on imports and as what an export policy
  announces. A set can **originate**: birdy renders a static protocol announcing
  its prefixes as blackhole/unreachable/prohibit anchors — because you must
  originate what you announce. A set can also be **expanded from an IRR `AS-SET`**
  with `bgpq4` (when `--bgpq4` is enabled); the source AS-SET is recorded so it can
  be refreshed. Tick **Auto-refresh from IRR** and birdy re-expands the set on
  `--irr-refresh-interval` (default daily), updating the model when the expansion
  changes — it **never applies on its own**, so the change waits on the Changes page
  for you to review and apply. The form shows the last sync time and any error; an
  empty expansion is treated as a mirror failure and the previous list is kept.
  A set can be **disabled** from the list (the power toggle, as on a peer) to switch
  it off without deleting it: while disabled, birdy renders neither its `define` nor
  its originator, so you stop announcing an aggregate and can flip it back on later
  with its prefixes intact. A policy that references a disabled set **drops the
  reference** rather than failing to parse — an **export** policy just stops
  announcing that set, and an **import allow-list** (*accept only from set*) **fails
  closed**, permitting nothing rather than accepting everything. Lint flags both so
  the effect is never silent. The bogon lists cannot be disabled; they are wired into
  every generated filter.
- **AS sets** — named lists of AS numbers (and ranges). This is where an expanded
  IRR `AS-SET` lands, since BIRD has no `AS-SET` concept. Point an import policy at
  one to accept a customer's downstreams by origin AS: the prefix set says *which
  prefixes*, the AS set says *from which origins*. Like a prefix set, it can be
  **expanded from its IRR `AS-SET`** with `bgpq4` — **Expand from IRR** on the form
  fills the members in for review, and **Auto-refresh from IRR** keeps them current
  on `--irr-refresh-interval`. **Refresh now** (the ↻ on the list) re-expands a set
  on demand. All of it updates the model only: the change waits on the Changes page
  for you to apply. Hand-written notes on a member survive a refresh, and an empty
  expansion — what `bgpq4` returns for an unknown AS-SET — is treated as a mirror
  failure and the previous members are kept, because an empty set would reject every
  route.
- **Communities** — named BGP communities: define a value once (standard
  `ASN:value` or large `ASN:x:y`, RFC 8092), give it a readable name, and reuse it.
  Each renders to a BIRD `define`. **Reference it by name** in a peer's export
  communities or a policy's match-community field (mixed with literals), and the
  rendered filter reads by name — `bgp_community.add(BLACKHOLE)` rather than a bare
  tuple. An unknown name is caught when you save, and a community cannot be deleted
  while a peer or policy still references it. The well-known `BLACKHOLE` (RFC 7999)
  and `GRACEFUL_SHUTDOWN` (RFC 8326) are seeded.
- **Static routes** — reachability nothing discovers on its own: a subnet behind a
  non-BGP device, or a route to a far router's loopback so an iBGP session peering
  on loopbacks can resolve its next hop. One route per prefix; action is `via`,
  `blackhole`, `unreachable`, or `prohibit`.

---

## 10. RPKI

Configure the **RTR servers** that feed BIRD the ROA table used for origin
validation. Running a local validator (Routinator, StayRTR, rpki-client) is the
usual production answer; a public RTR endpoint is fine to start with. Timers left
at `0` mean "leave BIRD's default alone". Validation itself is turned on per
import policy via the **RPKI ROV** knob (log-only or drop-invalid).

**Which policies validate** is a table of every import policy: its RPKI mode, what
that does to an invalid route, and — the part that matters before you enforce — the
peers riding on it. Policies doing *nothing* are listed too: one that carries half
your sessions and never checks an origin is exactly what you came here to find.

While any policy is in **log-only** mode, the page also runs the **dry run**. BIRD
counts the routes it is tagging invalid right now (`show route where … count`, so
nothing walks the RIB across the socket), and the panel leads with the number:
*"742 would be dropped"*, broken down per table. Below it, the routes themselves,
paginated — which prefixes, from which peers. The number is the answer; the list is
the evidence. Both exist so you can size the impact before switching a policy from
log-only to drop-invalid.

---

## 11. BMP monitoring

The **BGP Monitoring Protocol** ([RFC 7854](https://www.rfc-editor.org/rfc/rfc7854))
streams your sessions' route data and up/down events to an external collector — a
route collector, an analytics pipeline, or a looking-glass backend. BIRD's exporter
monitors **every** BGP session on the router automatically; a **station** just says
where the stream goes.

Add a station on the **BMP** page. Each row renders one `protocol bmp` instance:

- **Name** — the BIRD protocol name.
- **Station address / port** — the collector's IP (an address, not a hostname) and
  TCP port. BMP is `1790` by convention.
- **Monitor pre-policy RIB** — mirror what a peer sent, before your import filters
  ran.
- **Monitor post-policy RIB** — mirror what survived the import filters. Sending both
  lets the collector see exactly what your policies dropped. With neither, the
  collector still receives session state and statistics, just no route contents.
- **Send buffer limit (MB)** — how much data may queue for a slow collector before
  BIRD drops and restarts the station rather than run the router out of memory. `0`
  uses BIRD's default (1024). A disabled station is not rendered at all.

> [!NOTE]
> BMP is a **preliminary** feature in BIRD (2.14+), and the daemon must be built with
> BMP support. birdy renders the config; the `bird -p` check on the Changes page is
> what confirms your build accepts it.

---

## 12. Alerts

Session events (a session dropping, recovering, flapping, hitting its import
limit, a sharp drop in accepted prefixes, or a config being applied/reverted) are
delivered to any number of **destinations**: Slack, Discord, email (SMTP), or a
generic JSON webhook. Each destination can filter which event kinds it wants, and
repeats for the same session are suppressed within `--alert-cooldown`. birdy also
alerts when **BIRD itself becomes unreachable** — the one failure a
session-watching alert can't catch on its own. Two more kinds fire from the
background loops: **config drift** when `bird.conf` changes outside birdy (a hand
edit, a `birdc` reconfigure, or an unseen revert — deduped to one alert per
distinct drift), and an **IRR refresh** notice when an auto-refresh prefix set
changed. Manage destinations on the **Alerts** page; test one with the "Test"
button.

---

## 13. Settings

- **Router identity** — the **router label** (what to call this router; it names the
  router in alerts and is never rendered into `bird.conf`), the router ID (an
  IPv4-formatted 32-bit value) and the local ASN. The last two open every rendered
  config; BIRD will not start without a router ID. Optionally a route-reflector
  cluster ID. Note the big name on the dashboard is *not* the label — that is the
  system hostname, which BIRD reports and birdy only displays.
  - **Kernel source (IPv4/IPv6)** — a preferred source address pinned on the routes
    birdy installs into the kernel FIB (`krt_prefsrc`). It is the source the kernel
    stamps on the router's *own* traffic to those destinations — set it to a stable,
    announced address (typically a loopback) so control-plane traffic does not leave
    with whatever egress interface address the kernel would otherwise pick. Set per
    family, because `kernel4` and `kernel6` are separate protocols. Leave a field
    blank and that channel exports only Birdy-originated static routes (if BGP
    installation is enabled) without stamping a source. The address must be one
    actually configured on the box, or the kernel refuses to install the route.
  - **Install selected BGP routes (IPv4/IPv6)** — explicitly synchronizes BIRD's
    selected BGP route for each prefix into the corresponding Linux FIB. It never
    renders a blanket `export all`. Existing routers have both families
    auto-enabled during upgrade so behaviour is preserved; fresh installs default
    off. Birdy-originated static routes (aggregates, library statics) are always
    installed whenever any kernel export is active. A full-table peer can
    therefore install a full Internet table. This controls route installation
    only: Linux forwarding, firewall policy, underlay host routes, and capacity
    monitoring remain operator responsibilities. Birdy excludes any imported
    prefix covering the router ID, configured peer addresses, local session
    addresses, or preferred source so a full table cannot override the routes
    that keep the BGP control plane reachable. The default route (0.0.0.0/0,
    ::/0) is exempt from these guards — it covers every address by definition
    and is not a targeted control-plane hijack.
- **Bogons** — the bogon prefix lists (v4/v6) and bogon ASN list. Generated filters
  name these directly, which is why they live here rather than in the Library and
  cannot be deleted or announced. "Restore defaults" resets them to what birdy
  ships with.
- **Raw configuration** — an escape hatch appended verbatim to the end of the
  rendered `bird.conf`, for anything birdy does not model (extra tables, BFD
  tuning, graceful-restart options). birdy understands none of it; its only gate is
  `bird -p`, which runs before it saves.
- **Access control** — an application-level IP allow-list: one IP or CIDR per line,
  and only those may reach birdy at all. A blocked client gets **no response** — the
  connection is closed — and the gate covers *every* request, including the
  unauthenticated `/metrics`. Useful when birdy is bound to a public address with no
  host firewall. It defaults to `0.0.0.0/0` (allow all), and both an empty list and a
  `0.0.0.0/0` entry mean no restriction; the gate matches the real TCP peer, never a
  spoofable `X-Forwarded-For`. **You cannot lock yourself out**: loopback is always
  allowed, so an SSH tunnel (`ssh -L 8080:127.0.0.1:8080`) always works, and the page
  shows your connecting IP so you can add it before restricting.
- **Database snapshots** — birdy's entire state is one SQLite file. A consistent
  snapshot is taken nightly and can be downloaded on demand; the **backup bundle**
  also includes the rendered config. A snapshot can be staged for restore (applied
  on the next restart).
- **Updates** — under System → Updates, choose **Stable releases** to compare the
  installed version with GitHub's latest release, or **Development branch** to
  compare the embedded build commit with upstream `main`. Results are cached for
  15 minutes. The setting controls notifications only: birdy does not replace its
  own executable or run code fetched by the web process. Development builds must
  embed their source revision with the documented build `-ldflags` for an exact
  comparison.
- **Remote dashboard access** — Settings → General can generate a token that
  grants dashboard data only. On a central Birdy panel, open System → Instances
  and add the remote URL and token. The top-bar selector changes the dashboard
  target; remote targets are view-only and all peer, policy, settings, apply and
  backup actions remain tied to This Birdy. Use HTTPS and include the central
  panel's address in the remote instance's access whitelist. Rotating a remote
  token invalidates existing connections immediately.

### Navigating and editing safely

- The sidebar can be collapsed on wide screens and shows contextual links for
  the section you are working in. Its state, compact table density, selected
  theme, and dashboard columns are stored in the browser.
- Press **Ctrl/Cmd+K** to open the command palette. It searches pages and common
  creation actions; arrow keys select a result and Enter opens it.
- The top bar always identifies the selected router. A banner appears while a
  remote dashboard is selected, and management actions continue to target the
  local Birdy only.
- Peer, policy, library, RPKI, BMP, and alert editors warn before discarding
  unsaved changes. Peer profiles fill conservative role-specific limits and
  transport safeguards, but deliberately leave route policy selection, saving,
  validation, and apply under operator control.
- The peer editor summarizes identity, policy, import-limit, and transport
  readiness. The Changes page separately summarizes render, syntax, policy, and
  apply readiness; these indicators explain state but never bypass the existing
  lint acknowledgement or BIRD auto-revert.
- Settings → Theme selects the refreshed Modern style or the original Birdy
  visual style. Light and dark mode are independent of that selection.

---

## 14. Your profile

Reach it from the avatar menu at the top right → **Profile**.

- **Username** — the name you log in with. Changing it does not sign you out.
- **Password** — changing it requires your current password plus a confirmation,
  and a minimum of 8 characters. Requiring the current password means a stolen
  session alone cannot lock you out.

---

## 15. Security

birdy is a thin, single-user admin panel. Treat it as sensitive as `bird.conf`
itself.

- **Bind it to loopback and reach it over SSH.** It listens on `127.0.0.1:8080` by
  default. It has **no TLS**; a session cookie and a bcrypt password hash are the
  login's only defence (though every write action is recorded — see the audit trail
  below). Prefer the tunnel; if you must bind to a LAN address, understand exactly
  what you are exposing.
- **Restrict access by IP** with the allow-list under Settings → Access control
  (§13). It refuses every request from an address you did not list — the connection
  is closed with **no response at all**, and it covers the unauthenticated
  `/metrics` too. Loopback is always allowed, so an SSH tunnel can never lock you
  out, and the page shows your connecting IP so you can add it before restricting.
- **Every operator action is audited.** Peer, policy, community and settings
  changes, config applies and reverts are written to the event timeline attributed
  to the user who made them — a record of who changed what, and when.
- **BGP MD5 passwords are stored in the clear** in birdy's SQLite database, because
  that is the form BIRD needs them in. The database file is therefore as sensitive
  as `bird.conf`. Passwords are never rendered back into the browser: the peer form
  shows a blank "unchanged" field, and both sides of the config diff are masked.
- **The `/metrics` endpoint is unauthenticated** when enabled. Put it behind your
  own network controls, or the access allow-list above.
- **Run with `--read-only`** until you have a specific reason to let birdy write
  `bird.conf`. Applying is gated behind the authorship guard and the armed
  auto-revert, but read-only removes the possibility entirely.

---

*Found something wrong or missing? Open an issue or a pull request — see the
[README](../README.md).*
