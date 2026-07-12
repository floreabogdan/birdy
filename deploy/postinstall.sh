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

# State directory for the SQLite database (holds BGP session passwords in the
# clear — see the README — so it is birdy's alone).
mkdir -p /var/lib/birdy
chown birdy:birdy /var/lib/birdy 2>/dev/null || true
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

It ships read-only (a viewer). Reach it over an SSH tunnel; never bind it to a
public address. See /usr/share/doc/birdy/README.md.
EOF
fi

exit 0
