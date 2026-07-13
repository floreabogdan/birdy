package main

import (
	"log/slog"
	"os/exec"

	"github.com/floreabogdan/birdy/internal/netdiag"
)

// Optional capabilities are ON by default and detected from the environment at
// startup. birdy should work out of the box: install the package, and whatever
// the router can do, birdy offers. Editing a systemd unit to switch a feature on
// is not a setup step anyone should have to discover.
//
// A capability that needs a binary is enabled only if that binary is there —
// no bgpq4, no IRR expansion, and the UI says why rather than failing at the
// click. Each flag can still force the answer: --bgpq4 off, --netdiag=false,
// --peeringdb=false, --metrics=false, or an explicit path.

// features is what this router turned out to be able to do.
type features struct {
	Bgpq4Bin  string // path/name of bgpq4, empty when unavailable or disabled
	NetDiag   bool   // ping and/or traceroute are installed
	PeeringDB bool   // dials peeringdb.com — no local dependency
	Metrics   bool   // /metrics endpoint (gated on the access list, see web)
}

// detectFeatures resolves the flags against what is actually installed and logs
// the verdict, so the journal answers "why is IRR expansion missing?" without a
// support round trip.
func detectFeatures(bgpq4Flag string, netDiag, peeringDB, metrics bool, log *slog.Logger) features {
	f := features{PeeringDB: peeringDB, Metrics: metrics}

	switch bgpq4Flag {
	case "off", "false", "none":
		log.Info("feature: IRR expansion disabled by flag")
	case "auto", "":
		if bin, err := exec.LookPath("bgpq4"); err == nil {
			f.Bgpq4Bin = bin
			log.Info("feature: IRR expansion enabled", "bgpq4", bin)
		} else {
			log.Info("feature: IRR expansion off — bgpq4 is not installed (apt install bgpq4)")
		}
	default:
		// An explicit path is a promise the operator made; honour it even if the
		// lookup fails now, so a binary installed later starts working without a
		// restart-with-different-flags dance.
		f.Bgpq4Bin = bgpq4Flag
		log.Info("feature: IRR expansion enabled", "bgpq4", bgpq4Flag)
	}

	if netDiag {
		f.NetDiag = netdiag.Available()
		if f.NetDiag {
			log.Info("feature: diagnostics enabled", "tools", netdiag.AvailableTools())
		} else {
			log.Info("feature: diagnostics off — neither ping nor traceroute is installed")
		}
	}
	return f
}
