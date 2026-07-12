# birdy — original design & roadmap (2026)

> [!NOTE]
> **This is the original design document, written at the project's start.** All four
> milestones below have shipped and birdy has moved past this plan. It is kept as a record
> of the design intent and the reasoning behind the data model — **not** as a description of
> the current feature set or schema. For what birdy does today, see the
> [README](README.md) and [`docs/USAGE.md`](docs/USAGE.md).
>
> A few things diverged from what actually shipped:
> - **Prometheus metrics** and **PeeringDB lookups** were shipped (both opt-in), though the
>   plan listed metrics as out of scope and never anticipated PeeringDB.
> - **Alerting** grew far past "one webhook, no SMTP": Slack, Discord, SMTP email and generic
>   webhooks, multiple destinations with per-kind filtering and cooldown, plus BIRD-unreachable,
>   config-drift and IRR-refresh alerts.
> - The **data model** below lists `communities`, `functions` and `peer_templates` tables that
>   were never built as tables (communities became inline value parsing; peer templates became
>   "clone a peer"). Several shipped features — BMP, IRR/bgpq4 expansion and auto-refresh, RTBH,
>   BFD, GTSM, graceful restart, route-history sampling, the split `birdy.d/` layout, and
>   seed-from-BIRD — are not mentioned here at all.

One Go binary that runs **on the router** and gives you a web UI to manage BIRD 2.x:
eBGP/iBGP sessions, import/export policy, RPKI validation — plus live visibility into
what every session is doing. No agents, no controller, no fleet. One router, done well.

Guiding rule: **each milestone is independently usable and cannot break anything the
previous one didn't already touch.** You trust it as a viewer before it ever writes.

## Architecture

```
┌─ Debian router ────────────────────────────────────────────┐
│                                                            │
│  birdy (single static binary, systemd service)             │
│  ├── embedded web UI  (go:embed, no node build step)       │
│  ├── SQLite           (modernc.org/sqlite, pure Go, 1 file)│
│  ├── renderer         (SQLite model → full bird.conf)      │
│  └── BIRD client      (control socket /run/bird/bird.ctl)  │
│                              │                             │
│  BIRD 2.x  ◄── /etc/bird/bird.conf (owned by birdy)        │
└────────────────────────────────────────────────────────────┘
```

- **Config ownership**: birdy renders the *entire* `bird.conf` from its database. Clean
  start — no importing of existing configs. Adopting a router is an explicit step that
  backs up whatever is there first.
- **BIRD access**: native client for the control socket (the same line-based protocol
  `birdc` speaks). Used for `show protocols`, route queries, `configure`.
- **Deployment**: `GOOS=linux go build`, scp one file, `birdy init` (creates DB + admin
  password), install systemd unit. Upgrades = replace binary, restart.
- **Access/security**: binds `127.0.0.1:8080` by default (use an SSH tunnel); optional
  LAN bind with password login (session cookie, bcrypt hash in DB). Never public.

## Data model (SQLite)

| Table | Contents |
|---|---|
| `settings` | local ASN, router ID, listen addr, password hash, BIRD socket path, alert webhook URL |
| `events` | session state transitions, flaps, prefix-limit hits, config applies — one timeline, time-based retention |
| `peers` | name, description, type (ebgp/ibgp), neighbor v4/v6, remote AS, local address, multihop, MD5 password, passive, per-AF enable, prefix limits, import/export policy refs, enabled flag |
| `prefix_sets` / `as_sets` | named sets: prefixes with length ranges (`{ 10.0.0.0/8+ }` style), ASN lists |
| `communities` | named community values (standard + large) with descriptions, usable in rule matches and actions |
| `functions` | reusable filter logic — builder-generated or raw BIRD code |
| `peer_templates` | BIRD `template bgp` blocks: session defaults + attached policies, inherited by peers |
| `policies` / `policy_rules` | ordered match→action rules; match: prefix-set, AS-set, AS-path, community, ROA state, function call; action: accept, reject, set local-pref/MED, prepend, add/strip community; default action |
| `rpki_servers` | RTR host, port, refresh interval |
| `config_versions` | full rendered text, timestamp, note, applied/failed status |

Policies are reusable: define "transit import" once, attach to many peers, per direction.
Every policy edit shows the **generated BIRD filter code live** next to the form. A raw
BIRD-snippet escape hatch exists for anything the builder can't express (still validated).

### Library & starter pack

Sets, communities, functions, filters and peer templates are first-class objects (the
"Library"), rendered in dependency order: `define`s/sets → functions → filters →
templates → protocols. birdy seeds a **starter pack** on init — ordinary editable rows
tagged `builtin`, so you can use, clone, or change them:

- **Prefix sets**: bogons/martians v4 and v6 (RFC 1918, special-use, documentation,
  link-local…), default-route-only, "too-specific" length guards
- **AS sets**: bogon ASNs (0, 23456, private 64512–65534, reserved ranges)
- **Communities**: well-known ones named (NO_EXPORT, NO_ADVERTISE, BLACKHOLE 65535:666,
  GRACEFUL_SHUTDOWN 65535:0) + your own scheme — e.g. informational tags ("learned from
  transit X") set on import and matched on export, action communities ("prepend 3× to Y")
- **Functions**: `is_bogon_v4()`, `is_bogon_v6()`, `is_default()`, prefix-length sanity,
  bogon-ASN-in-path — generated from the sets above, or hand-written raw BIRD code
- **Peer templates**: "eBGP transit", "IX peer", "iBGP mesh" — creating a peer is: pick
  template, enter neighbor IP + AS, done (maps to BIRD's native `template bgp` inheritance)

Static martian/special-use lists are stable and shippable. Dynamic *fullbogons* (unallocated
space, needs a live feed) are out of scope for now.

## Apply pipeline (M2)

1. Render full config → temp file
2. `bird -p -c <tmp>` parse check — fail = nothing happens
3. UI shows **unified diff** (live file vs. candidate); user clicks Apply
4. Timestamped backup of current file, atomic write of new one
5. `configure timeout 90` via socket — BIRD itself reverts if not confirmed
6. birdy verifies: previously-established sessions re-establish, route counts sane
7. OK → `configure confirm` + record version; not OK → `configure undo` + restore file, surface the error

Every version is kept; any version can be diffed and rolled back to from the UI.

## Screens

- **Dashboard** — session grid: state, uptime, flaps, prefixes imported/exported/preferred
  vs. limit, last error. Live (poll socket every few seconds). Below it: the **event
  timeline** — session ups/downs, flaps, limit hits, and config applies on one axis, so
  "session dropped 40s after change #12" is visible at a glance.
- **Peers** — list + create/edit form (eBGP and iBGP); create from a peer template so a
  new session is just neighbor IP + AS. Per-peer ops buttons: enable / disable / restart,
  and **drain** — tag announcements GRACEFUL_SHUTDOWN (65535:0) + optional prepend so
  traffic shifts away before maintenance; un-drain restores.
- **Policies** — rule builder with live BIRD-code preview.
- **Library** — prefix sets, AS sets, communities, functions, peer templates; starter
  pack seeded, everything editable, BIRD-code preview on every object.
- **RPKI** — RTR server status, valid/invalid/unknown counts, and a **dry-run report**:
  "these N routes would be dropped" *before* you switch a peer from log-only to enforce.
- **Looking glass** — on-demand route queries: by prefix, by peer, imported vs. exported,
  "why was this rejected". Never syncs the full table (~2.6M routes) — query, paginate.
- **Changes** — pending diff, apply, history, rollback.

## Milestones

**M1 — Observe (read-only, zero write risk)**
Binary + systemd unit + login + BIRD socket client + dashboard + session detail +
looking glass. Event log + timeline (persist every state transition seen while polling).
Safety kit: `birdy doctor` preflight (bird version, socket access, `bird -p` present,
`/etc/bird` writable, systemd health), `--read-only` flag to run as a pure viewer,
nightly SQLite snapshot + download/restore in the UI (whole state = one file).
Deployable to the real router on day one; touches nothing.

**M2 — Manage (first write)**
Peers/policies/library CRUD, renderer, full apply pipeline with rollback, history.
Per-peer ops: enable / disable / restart / drain (graceful-shutdown + prepend).
Explicit one-time "adopt this router" step that backs up the existing config.

**M3 — Protect (RPKI)**
`protocol rpki` + `roa4/roa6` tables from configured RTR servers (start with public
ones, e.g. rtr.rpki.cloudflare.com), per-peer ROV: off → log-only → enforce, dry-run
report, dashboard counts.

**M4 — Notify**
One webhook URL (ntfy/Telegram/Slack-style plain POST) fired on session down,
prefix-limit hit, or RPKI-invalid spike. No SMTP, no alert engine.

**Explicitly out of scope (later, maybe):** multi-router/central controller, GRE/interface
management, importing existing configs, Prometheus metrics export, log/journal viewer,
auto-updating fullbogon feeds, multi-user accounts.

## Tech choices

- Go stdlib `net/http`, `html/template`; small vanilla JS for interactivity; `go:embed`
  for all assets — no frontend toolchain.
- Light-first modern SaaS theme with dark-mode toggle; monospace for all network data.
- `modernc.org/sqlite` (no cgo → trivial cross-compile from Windows to linux/amd64).
- Only other deps as needed: bcrypt (x/crypto). Keep the go.mod short.
