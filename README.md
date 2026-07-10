# birdy

A single Go binary that runs **on your router** and gives you a web UI for [BIRD 2.x](https://bird.network.cz/):
eBGP/iBGP sessions, import/export policy, RPKI origin validation — plus live visibility into what
every session is actually doing.

No agents. No controller. No fleet. One router, done well.

---

## Read this before you install it

**birdy is beta software. Expect bugs.** It is a personal project released in the hope it is useful
to someone else. Nothing here has been through the kind of testing a piece of routing infrastructure
deserves.

**Do not point birdy at a router with a configuration you care about.** birdy does not import,
merge with, or preserve an existing `bird.conf`. It renders the *entire* config file from its own
database. Anything it does not know about — a protocol, a filter, a table, a hand-tuned option —
does not exist as far as birdy is concerned, and would be gone from any config it wrote. Use it on a
**new router**, or on one whose config you are content to re-create inside birdy from scratch.

**birdy is opinionated.** It does not expose every knob BIRD has. It renders what its authors believe
is good practice — RFC 8212 default-deny on export, bogon prefix and ASN filtering, large communities
to tag route origin, enforce-first-AS on eBGP, next-hop-self on iBGP, RPKI invalid drop — and it will
happily refuse to render a config it thinks is a route leak. Anything it does not model goes in a raw
block, appended verbatim. If you disagree with those opinions, birdy is the wrong tool and you should
write `bird.conf` by hand. That is a perfectly good way to run a router.

**There is no support.** No warranty, no SLA, no guarantee of fitness for anything. Issues and pull
requests are welcome and may be ignored. If you run this and it breaks your BGP session, your
transit, your customers, or your night's sleep, that is entirely your responsibility. You accepted
that the moment you ran it. See [LICENSE](LICENSE).

---

## What works today

birdy is currently **read-only**. It has never written a byte to `/etc/bird/bird.conf` and cannot;
the code to do so is not written yet. That is deliberate — you get to trust it as a viewer long
before it is allowed to touch anything.

**Observe**
- Live dashboard of every BIRD protocol, split into BGP sessions and infrastructure
- Per-peer detail: BGP state, channels, import limits, and the raw control-socket output
- Route browser per session — imports, exports, and what was rejected on export
- On-demand looking glass (`show route for …`)
- Timeline of session transitions, flaps, and prefix-limit hits

**Model**
- Peers with roles (upstream, IX peer, customer, iBGP), which drive automatic origin tagging
- iBGP with next-hop-self and route reflection
- Composable import and export policy chains, rather than one policy per session
- A library of prefix sets and AS sets
- Bogon prefixes and bogon ASNs, editable, in Settings
- RPKI: RTR servers and per-policy validation (log-only or drop-invalid)
- A raw config block for everything birdy does not model, checked by `bird -p` before it saves

**Preview**
- The whole candidate `bird.conf`, rendered from the model, with a syntax check via `bird -p`
- A unified diff against the running config
- A linter for what `bird -p` cannot catch: route leaks, sessions that would accept nothing,
  unreachable filter branches, an RTR server nobody validates against

To apply any of it, copy the config out of the **Changes** page by hand. Passwords are masked in
the browser, so fill in the real MD5 secrets yourself.

**Not built yet:** writing `bird.conf`, the apply pipeline (`configure check` / `timeout` / `confirm`
/ `undo`, backup, rollback), peer templates, community manipulation and prepending, and alerting.

**Not modelled, so it belongs in the raw block:** BFD, graceful restart tuning, extra routing tables,
IGPs (OSPF, Babel), MPLS, and restricting which interfaces the `direct` protocol picks up.

## Install

Requires Go 1.25+ to build. The binary is static (`CGO_ENABLED=0`); SQLite is
[modernc.org/sqlite](https://modernc.org/sqlite), so there is nothing to link against.

```sh
go install github.com/floreabogdan/birdy/cmd/birdy@latest
```

Or cross-compile from anywhere and copy one file to the router:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o birdy ./cmd/birdy
scp birdy root@router:/usr/local/bin/birdy
```

Then, on the router:

```sh
birdy doctor                       # preflight: can it reach BIRD? can it read what it needs?
birdy init --asn 64496 --router-id 192.0.2.1 --label rtr1
birdy server --read-only           # or install deploy/birdy.service
```

`birdy init` prompts for an admin password. It reads BIRD's control socket
(`/run/bird/bird.ctl` by default), so it needs to run as a user in BIRD's group.

A sample systemd unit is in [`deploy/birdy.service`](deploy/birdy.service). It runs birdy as an
unprivileged `birdy` user in group `bird`, with `ProtectSystem=strict`.

## Security

birdy listens on `127.0.0.1:8080` by default. **Reach it over an SSH tunnel.** If you bind it to a
LAN address, understand what you are exposing: a session cookie and a bcrypt password hash are the
only things between the internet and your BGP config. It has no TLS, no rate limiting, and no audit
log. Never put it on a public address.

BGP MD5 session passwords are stored **in the clear** in birdy's SQLite database, because that is
the form BIRD needs them in. The database file is therefore as sensitive as `bird.conf` itself.
Passwords are never rendered into the browser: the peer form shows a blank field meaning
"unchanged", and both sides of the config diff are masked.

Run it with `--read-only` until you have reason not to.

## Development

```sh
go test ./...
```

All addresses and AS numbers in the test fixtures are from the documentation ranges of
[RFC 5398](https://www.rfc-editor.org/rfc/rfc5398), [RFC 5737](https://www.rfc-editor.org/rfc/rfc5737)
and [RFC 3849](https://www.rfc-editor.org/rfc/rfc3849).

The UI is server-rendered `html/template` with `go:embed` and a little vanilla JavaScript. There is
no node build step and there will not be one.

[`PLAN.md`](PLAN.md) has the roadmap and the reasoning behind the data model.

## License

[BSD Zero Clause](LICENSE) — public-domain-equivalent. Do whatever you like with it; you owe no
attribution and get no warranty.

The bundled webfonts are [IBM Plex](https://github.com/IBM/plex), copyright IBM Corp., used under
the SIL Open Font License 1.1 — see [`internal/web/static/fonts/LICENSE.txt`](internal/web/static/fonts/LICENSE.txt).
That license covers the fonts only, not birdy.
