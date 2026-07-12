# Contributing to birdy

Thanks for your interest. birdy is a personal project released in the hope it is useful to
someone else. Issues and pull requests are welcome — and, as the README says, may be ignored
or declined. Please read this first so we don't waste each other's time.

## Before you start

birdy is **deliberately opinionated**. It does not expose every knob BIRD has; it renders what
its author believes is good practice (default-deny on export, bogon filtering, large-community
origin tagging, enforce-first-AS, next-hop-self on iBGP, RPKI invalid drop) and will refuse to
render a config it thinks is a route leak. It also keeps scope tight — one router, done well; no
multi-router controller, no interface management, no importing a hand-written `bird.conf`.

If your change adds a knob, a dependency, or surface area, **open an issue first** and describe
the use case. A small, focused change that fits the existing grain is far more likely to land
than a large one that broadens the project's scope. If you disagree with the opinions, hand-writing
`bird.conf` is a perfectly good way to run a router, and forking is free (it's 0BSD).

## Development

Requires Go 1.25+. There is no frontend build step — the UI is server-rendered `html/template`
with `go:embed` and a little vanilla JavaScript, and there will not be a node toolchain.

```sh
go test ./...          # the whole suite
gofmt -l .             # must print nothing
go vet ./...           # must pass
golangci-lint run      # if you have it installed; CI runs it
```

Before opening a PR, make sure `gofmt`, `go vet`, `golangci-lint` and `go test ./...` all pass —
CI runs exactly these and will reject a red build.

Cross-compile to try it on a router (static binary, no cgo):

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o birdy ./cmd/birdy
```

## House conventions

- **Comments explain *why*, not *what*.** birdy's comments carry the reasoning behind a decision;
  match that. Don't add comments that restate the next line.
- **Keep the dependency list short.** The whole point is a single static binary; a new dependency
  needs a strong justification.
- **Validate at the model boundary.** Anything interpolated into `bird.conf` (peer names, prefixes,
  IRR objects) is validated in the store, not escaped downstream.
- **Never log or render secrets.** Session passwords are masked everywhere they could reach a browser
  or a chat webhook.
- **Tests come with behavior changes.** The store and web layers have good coverage; keep it that way.

## Pull requests

- Branch from `main`, keep the PR focused, and write a clear description of the problem and the fix.
- One logical change per PR. Unrelated cleanups are welcome but as separate PRs.
- By contributing you agree your work is released under the project's [0BSD license](LICENSE) —
  public-domain-equivalent, no attribution required. No CLA, no sign-off needed.

## Security

Please do **not** file security issues in public. See [SECURITY.md](SECURITY.md).
