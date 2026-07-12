#!/bin/sh
# Runs before the birdy package is removed (deb prerm / rpm preun / apk
# pre-deinstall). Stop and disable the service only on a real removal, not on an
# upgrade — the argument tells them apart:
#   deb:  "remove" (vs "upgrade")
#   rpm:  "0" for uninstall (vs "1" for upgrade)
#   apk:  no argument on deinstall
set -e

removing=no
case "${1:-}" in
	remove|deinstall|purge|0|"") removing=yes ;;
esac
# rpm/apk pass a count; treat only 0 as removal (handled above); any non-zero
# is an upgrade.
case "${1:-}" in
	[1-9]*) removing=no ;;
esac

if [ "$removing" = yes ] && command -v systemctl >/dev/null 2>&1; then
	systemctl stop birdy.service 2>/dev/null || true
	systemctl disable birdy.service 2>/dev/null || true
fi

exit 0
