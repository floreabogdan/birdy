# syntax=docker/dockerfile:1

# ── build ────────────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS build
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
FROM alpine:3.20
# ca-certificates: outbound TLS (PeeringDB, SMTP over TLS).
# bird: the "bird -p" parser birdy uses to syntax-check a candidate config.
#       This is NOT the running daemon — that stays on the host and is reached
#       over the mounted control socket.
RUN apk add --no-cache ca-certificates bird \
 && addgroup -S birdy \
 && adduser -S -G birdy -H -h /var/lib/birdy birdy \
 && mkdir -p /var/lib/birdy \
 && chown birdy:birdy /var/lib/birdy

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
