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

- **bgpq4** — enables one-click expansion of an IRR `AS-SET` into a prefix set.
  Off unless you pass `--bgpq4`.
- **A local RPKI validator** speaking RTR (Routinator, StayRTR, rpki-client) — for
  origin validation. You can start with a public RTR endpoint instead.
- **Outbound HTTPS** — only if you enable PeeringDB lookups (`--peeringdb`) or use
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

Then start birdy with `--bgpq4 bgpq4` (or the full path). Without it, prefix sets
still work; you just fill them in by hand instead of expanding an `AS-SET`.

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
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o birdy ./cmd/birdy
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
ExecStart=/usr/local/bin/birdy server --db /var/lib/birdy/birdy.db --read-only
ProtectSystem=strict
ReadWritePaths=/var/lib/birdy
```

Create the user and state directory:

```sh
sudo useradd --system --gid bird --home-dir /var/lib/birdy --shell /usr/sbin/nologin birdy
sudo install -d -o birdy -g bird /var/lib/birdy
```

If you later let birdy **apply** configs (drop `--read-only`), it also needs write
access to `bird.conf` and its directory — add that path to `ReadWritePaths`.

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
sudo -u birdy birdy doctor
```

**3. Run the server.** For a quick look, run it directly:

```sh
sudo -u birdy birdy server --read-only
```

For production, install the systemd unit:

```sh
sudo cp deploy/birdy.service /etc/systemd/system/birdy.service
sudo systemctl daemon-reload
sudo systemctl enable --now birdy
```

**4. Open the UI.** birdy listens on `127.0.0.1:8080` by default. Reach it over an
SSH tunnel — never expose it directly (see [Security](#14-security)):

```sh
ssh -L 8080:127.0.0.1:8080 user@router
# then browse to http://localhost:8080
```

Log in, and under **Settings → Router identity** confirm the router ID and local
ASN. birdy stays a read-only viewer until you decide otherwise.

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
| `--listen` | `127.0.0.1:8080` | Address the web UI listens on, stored in settings. |
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
| `--read-only` | `false` | **Run as a pure viewer** — never issue a write command to BIRD and never write `bird.conf`. Recommended until you trust it. |
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
| `--metrics` | `false` | Expose an **unauthenticated** Prometheus `/metrics` endpoint. Put it behind your own network controls. |
| `--peeringdb` | `false` | Enable PeeringDB lookups on the peer form (dials out to peeringdb.com). |
| `--bgpq4` | *(empty)* | Path to `bgpq4` to enable IRR `AS-SET` expansion on prefix sets. Empty disables it; `bgpq4` uses `PATH`. |
| `--drift-check-interval` | `30s` | How often to check whether `bird.conf` changed outside birdy, alerting if it did (`0` disables). Inert until birdy owns a config. |
| `--sample-interval` | `1m` | How often to record a per-session route-count point for the dashboard history sparklines (`0` disables). |
| `--sample-retain` | `168h` | How long to keep route-count history samples. |
| `--irr-refresh-interval` | `24h` | How often to re-expand auto-refresh prefix sets from IRR via `bgpq4` (`0` disables; requires `--bgpq4`). |

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
(`--apply-timeout`) to keep it, or do nothing and BIRD rolls back on its own. A
**soft** reload re-runs filters without bouncing sessions.

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
history sparklines from samples birdy records itself on `--sample-interval`
(pruned to `--sample-retain`) — enough to see when a session started leaking or
losing prefixes without a Prometheus/Grafana stack.

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
| **AS-path prepend** | eBGP | Prepend your own AS this many times (0–10) to everything you announce here. A longer path is less preferred, steering inbound traffic **away** from this peer. |
| **Export communities** | eBGP | Communities attached to every route you announce here — e.g. an upstream's "do not export to your other peers" signal. One per line, standard (`ASN:value`) or large (`ASN:x:y`). |
| **Drain** | eBGP | Signal RFC 8326 graceful shutdown to the peer and deprefer its routes, so traffic moves off the session before you take it down for maintenance. Does **not** disable the session. |
| **Next-hop-self** | iBGP | Readvertise routes with your own address as the next hop. On by default: without it, an eBGP route carries the *external* peer's address, which the far end of the iBGP session usually cannot reach. Leave it on unless your IGP carries the peering subnets. |
| **Route reflector client** | iBGP | Reflect iBGP routes to this peer, lifting the rule that stops readvertising iBGP routes to other iBGP peers. Set a cluster ID under Settings if you run more than one reflector. |
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
- **AS sets** — named lists of AS numbers (and ranges). This is where an expanded
  IRR `AS-SET` lands, since BIRD has no `AS-SET` concept. Point an import policy at
  one to accept a customer's downstreams by origin AS.
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

- **Router identity** — router ID (an IPv4-formatted 32-bit value) and local ASN.
  These open every rendered config; BIRD will not start without a router ID.
  Optionally a route-reflector cluster ID.
- **Bogons** — the bogon prefix lists (v4/v6) and bogon ASN list. Generated filters
  name these directly, which is why they live here rather than in the Library and
  cannot be deleted or announced. "Restore defaults" resets them to what birdy
  ships with.
- **Raw configuration** — an escape hatch appended verbatim to the end of the
  rendered `bird.conf`, for anything birdy does not model (extra tables, BFD
  tuning, graceful-restart options). birdy understands none of it; its only gate is
  `bird -p`, which runs before it saves.
- **Database snapshots** — birdy's entire state is one SQLite file. A consistent
  snapshot is taken nightly and can be downloaded on demand; the **backup bundle**
  also includes the rendered config. A snapshot can be staged for restore (applied
  on the next restart).

---

## 14. Your profile

Reach it from the avatar menu at the top right → **Profile & password**.

- **Username** — the name you log in with. Changing it does not sign you out.
- **Password** — changing it requires your current password plus a confirmation,
  and a minimum of 8 characters. Requiring the current password means a stolen
  session alone cannot lock you out.

---

## 15. Security

birdy is a thin, single-user admin panel. Treat it as sensitive as `bird.conf`
itself.

- **Bind it to loopback and reach it over SSH.** It listens on `127.0.0.1:8080` by
  default. It has **no TLS and no audit log**; a session cookie and a bcrypt
  password hash are the only things between a caller and your BGP config. Never put
  it on a public address. If you must bind to a LAN address, understand exactly
  what you are exposing.
- **BGP MD5 passwords are stored in the clear** in birdy's SQLite database, because
  that is the form BIRD needs them in. The database file is therefore as sensitive
  as `bird.conf`. Passwords are never rendered back into the browser: the peer form
  shows a blank "unchanged" field, and both sides of the config diff are masked.
- **The `/metrics` endpoint is unauthenticated** when enabled. Put it behind your
  own network controls.
- **Run with `--read-only`** until you have a specific reason to let birdy write
  `bird.conf`. Applying is gated behind the authorship guard and the armed
  auto-revert, but read-only removes the possibility entirely.

---

*Found something wrong or missing? Open an issue or a pull request — see the
[README](../README.md).*
