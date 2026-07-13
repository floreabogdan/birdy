#!/bin/sh
# Runs after install/upgrade of the birdy package (deb postinst / rpm post /
# apk post-install). Portable across the three: it tries each distro's user
# tools in turn. It never starts the service — birdy needs `birdy init` (an
# interactive admin password) first — but it will restart an already-running
# birdy so an upgrade takes effect.
set -e

# A system group and user for birdy, if not already present.
if ! getent group birdy >/dev/null 2>&1; then
	groupadd --system birdy 2>/dev/null \
		|| addgroup --system birdy 2>/dev/null \
		|| addgroup -S birdy 2>/dev/null || true
fi
if ! getent passwd birdy >/dev/null 2>&1; then
	useradd --system --gid birdy --home-dir /var/lib/birdy --shell /usr/sbin/nologin birdy 2>/dev/null \
		|| adduser --system --ingroup birdy --home /var/lib/birdy --no-create-home --disabled-login birdy 2>/dev/null \
		|| adduser -S -G birdy -H -h /var/lib/birdy birdy 2>/dev/null || true
fi

# birdy reads BIRD's control socket, which is group-owned by `bird`. Add it to
# that group if BIRD is installed.
if getent group bird >/dev/null 2>&1; then
	usermod -aG bird birdy 2>/dev/null || addgroup birdy bird 2>/dev/null || true
fi

# birdy renders and applies bird.conf from the UI, which means writing into
# /etc/bird as the `birdy` user (member of `bird`). The directory ships 0750
# root:bird or bird:bird, so the group cannot create the birdy.d/ includes and an
# apply would fail with a permissions error the operator then has to go and fix
# by hand. Grant the group write here instead — it is exactly the access birdy
# needs, and no wider than the `bird` group already has on the socket.
if [ -d /etc/bird ] && getent group bird >/dev/null 2>&1; then
	chgrp bird /etc/bird 2>/dev/null || true
	chmod g+w /etc/bird 2>/dev/null || true
fi

# State directory for the SQLite database (holds BGP session passwords in the
# clear — see the README — so it is birdy's alone).
#
# Recursive on purpose: "birdy init" run under sudo (which is what a human on a
# router does) leaves a root-owned birdy.db behind, and the service then runs as
# birdy and can read its own state but not write it — SQLite fails at the first
# write, which is the login, and the UI says "internal error". Newer birdy hands
# the file over at init time and refuses to start on an unwritable database, but
# an upgrade must also repair an install that already went wrong.
mkdir -p /var/lib/birdy
chown -R birdy:birdy /var/lib/birdy 2>/dev/null || true
chmod 750 /var/lib/birdy

if command -v systemctl >/dev/null 2>&1; then
	systemctl daemon-reload 2>/dev/null || true
	# Restart only if it is already running (an upgrade); a no-op on first install.
	systemctl try-restart birdy.service 2>/dev/null || true
fi

# Only print the getting-started hint on a fresh install, not every upgrade.
if [ ! -f /var/lib/birdy/birdy.db ]; then
	cat <<'EOF'

birdy is installed. It does not start on its own — set it up first:

  sudo -u birdy birdy init --db /var/lib/birdy/birdy.db --asn <YOUR_ASN> --router-id <ROUTER_IP>
  sudo systemctl enable --now birdy

It then listens on port 8080 on every interface and enables whatever this router
can do (bgpq4, ping/traceroute) — no flags to set, no unit to edit.

FIRST THING TO DO once you log in: Settings -> Access control. Until you list the
IPs allowed to reach birdy, it accepts connections from anywhere, and birdy has
no TLS, so the login travels in the clear. Set the allow-list (that also switches
/metrics on), or run it closed with --listen 127.0.0.1:8080 plus an SSH tunnel.

See /usr/share/doc/birdy/README.md.
EOF
fi

exit 0
