#!/bin/sh
# Runs after the birdy package is removed (deb postrm / rpm postun / apk
# post-deinstall).
set -e

if command -v systemctl >/dev/null 2>&1; then
	systemctl daemon-reload 2>/dev/null || true
fi

# Only a Debian `purge` deletes the state directory — it holds the SQLite
# database with BGP session passwords in the clear. A plain remove keeps it, and
# so does an rpm/apk uninstall: routine removal must never delete credentials.
if [ "${1:-}" = purge ]; then
	rm -rf /var/lib/birdy
fi

exit 0
