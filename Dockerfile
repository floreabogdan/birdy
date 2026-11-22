# syntax=docker/dockerfile:1

# ── build ────────────────────────────────────────────────────────────────
# Both base images are pinned by digest so a build is reproducible and a moved
# tag cannot change what ships; Dependabot (docker ecosystem) bumps the digests.
FROM golang:1.27-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS build
WORKDIR /src

# Cache modules first.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=0.1.0-dev
# CGO stays off — modernc.org/sqlite is pure Go, so the binary is static and
# needs nothing from the final image to run.
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags="-s -w -X github.com/floreabogdan/birdy/internal/buildinfo.Version=${VERSION}" \
      -o /out/birdy ./cmd/birdy

# ── runtime ──────────────────────────────────────────────────────────────
# Alpine's "bird2" package, not "bird": since Alpine 3.22 "bird" is BIRD 3, and
# the syntax check has to parse the config the way the BIRD 2.x daemon on the
# host will.
FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
# ca-certificates: outbound TLS (PeeringDB, SMTP over TLS).
# bird2: the "bird -p" parser birdy uses to syntax-check a candidate config.
#       This is NOT the running daemon — that stays on the host and is reached
#       over the mounted control socket.
# apk upgrade: take fixes published since the pinned base image was built.
#
# The birdy account keeps uid 101 / gid 102, the ids every image up to 0.7.0
# used, so an existing birdy-data volume stays owned by it across the upgrade.
# The state directory holds BGP session passwords in the clear, so it is
# birdy's alone (0750).
RUN apk upgrade --no-cache \
 && apk add --no-cache ca-certificates bird2 \
 && addgroup -S -g 102 birdy \
 && adduser -S -u 101 -G birdy -H -h /var/lib/birdy birdy \
 && install -d -o birdy -g birdy -m 0750 /var/lib/birdy

COPY --from=build /out/birdy /usr/local/bin/birdy

VOLUME /var/lib/birdy
EXPOSE 8080
USER birdy
ENTRYPOINT ["birdy"]
# Inside a container birdy must listen on all interfaces so a published port can
# reach it; control the exposure by choosing what you publish it to (see
# docker-compose.yml) and by setting the IP allow-list once you log in. Add
# --read-only here to run it as a pure viewer.
CMD ["server", "--listen", "0.0.0.0:8080"]
