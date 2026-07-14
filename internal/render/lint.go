package render

import (
	"fmt"
	"strings"

	"github.com/floreabogdan/birdy/internal/store"
)

// Severity of a lint finding. A Danger will not stop birdy rendering a config —
// bird -p would happily accept all of these — but each one describes a session
// that will not behave the way its author expects.
const (
	SeverityDanger = "danger"
	SeverityWarn   = "warn"
)

type Warning struct {
	Severity string
	Peer     string // empty for findings that are not about one peer
	Message  string
}

// Lint inspects the model for combinations that parse cleanly but mean
// something other than what the operator almost certainly intended.
//
// These checks exist because the policy model is composable: import policies
// compose with AND and export policies with OR, and both of those let you write
// a chain whose later members can never fire.
func Lint(in Input) []Warning {
	var out []Warning
	add := func(sev, peer, format string, args ...any) {
		out = append(out, Warning{Severity: sev, Peer: peer, Message: fmt.Sprintf(format, args...)})
	}
	asSets := map[int64]store.ASSet{}
	for _, as := range in.ASSets {
		asSets[as.ID] = as
	}
	prefixSets := map[int64]store.PrefixSet{}
	for _, ps := range in.PrefixSets {
		prefixSets[ps.ID] = ps
	}

	// A community referenced by name that is not defined in the library would
	// render as an undefined BIRD symbol. Deleting a referenced community is
	// prevented in the UI, but a snapshot restore or a hand-edited database can
	// still leave a reference dangling — catch it here, before bird -p does at
	// apply time.
	communityNames := map[string]bool{}
	for _, cd := range in.Communities {
		communityNames[cd.Name] = true
	}
	for _, p := range in.Peers {
		for _, name := range store.NamedCommunityRefs(p.ExportCommunities) {
			if !communityNames[name] {
				add(SeverityDanger, p.Name,
					"Export communities reference %q, which is not defined in the library — bird -p would reject the config with an undefined symbol. Define it under Library → Communities, or remove the reference.",
					name)
			}
		}
	}
	for _, pol := range in.Policies {
		for _, name := range store.NamedCommunityRefs(pol.MatchCommunity) {
			if !communityNames[name] {
				add(SeverityDanger, "",
					"Policy %s matches community %q, which is not defined in the library. Define it under Library → Communities, or remove the reference.",
					pol.Name, name)
			}
		}
	}

	// An eBGP peer whose ASN is our own is an iBGP session wearing the wrong
	// label: it gets the role tagging, the first-AS check and the bogon filters,
	// none of which make sense inside our own AS.
	for _, p := range in.Peers {
		switch {
		case p.IsIBGP() && p.RemoteASN != in.LocalASN:
			add(SeverityDanger, p.Name,
				"This peer is marked iBGP but its remote AS is %d, not our own AS%d. BIRD will open an eBGP session and none of the iBGP handling applies.",
				p.RemoteASN, in.LocalASN)
		case !p.IsIBGP() && p.RemoteASN == in.LocalASN:
			add(SeverityDanger, p.Name,
				"This peer carries our own AS%d but is not marked iBGP, so birdy renders eBGP filters for it — including the check that rejects our own ASN in the AS path.",
				in.LocalASN)
		}

		// The one that black-holes traffic rather than merely dropping routes.
		if p.IsIBGP() && !p.NextHopSelf {
			add(SeverityWarn, p.Name,
				"Next-hop-self is off. Routes learned from an eBGP peer will be readvertised on this session carrying that peer's address as the next hop; the far end can only use them if your IGP carries the peering subnets.")
		}

		// NOTE: deliberately no lint for "iBGP session without policies". Carrying
		// everything is the conventional full-mesh config and correct for most
		// routers, so warning about it would fire on the normal case — and a warning
		// you see every time is one you stop reading. The trap it would be pointing
		// at (the far router inheriting a default it should not have, and killing a
		// tunnel by routing the tunnel's own endpoint through it) is explained on the
		// peer form, where the decision is actually made.

		// A drained peer is a deliberate, temporary state — but an easy one to
		// forget, so surface it every time the config is reviewed.
		if p.Drained {
			add(SeverityWarn, p.Name,
				"This peer is draining: birdy signals graceful shutdown and deprefers its routes so traffic moves away. Undrain it once maintenance is done.")
		}
	}

	if in.RRClusterID != "" {
		var reflecting bool
		for _, p := range in.Peers {
			if p.RRClient {
				reflecting = true
			}
		}
		if !reflecting {
			add(SeverityWarn, "",
				"A route reflector cluster ID is set, but no peer is marked as a reflector client, so it is never rendered.")
		}
	}

	if strings.TrimSpace(in.RawConfig) != "" {
		add(SeverityWarn, "",
			"This config ends with a raw block that birdy does not understand. It is checked by bird -p and by nothing else — no lint rule here applies to it.")
	}

	// Two routes to the same net, from two different protocols. BIRD picks one on
	// preference (static beats static by nothing in particular) and never says
	// which, so the operator's intent is lost either way.
	originated := map[string]string{} // prefix -> set name
	for _, ps := range in.PrefixSets {
		if !ps.Originate {
			continue
		}
		for _, e := range ps.Entries {
			originated[e.Prefix] = ps.Name
		}
	}
	for _, r := range in.StaticRoutes {
		if !r.Enabled {
			continue
		}
		if set, clash := originated[r.Prefix]; clash {
			add(SeverityDanger, "",
				"%s is both a static route and an anchor originated by %s. Two protocols would offer the same route; delete one.",
				r.Prefix, set)
		}
	}

	// The one that leaks your internal deaggregates to the internet. An export
	// filter is "if net ~ SET then accept", so a "+" on the aggregate accepts
	// every more-specific inside it too — including the /26s you split it into
	// for your own routers. Nothing else stops them: birdy puts no prefix-length
	// guard on export, and by the time your upstream's filter catches it you are
	// relying on someone else's config to enforce your intent.
	for _, pol := range in.Policies {
		if pol.Direction != store.DirExport {
			continue
		}
		for _, id := range pol.SetIDs {
			ps, ok := prefixSets[id]
			if !ok {
				continue
			}
			for _, e := range ps.Entries {
				if e.Modifier == "" {
					continue
				}
				add(SeverityWarn, "",
					"%s announces %s. The %q matches more-specifics inside %s, so any subnet you split it into would be announced as well. Drop the modifier to announce the aggregate alone.",
					pol.Name, e.Pattern(), e.Modifier, e.Prefix)
			}
		}
	}

	for _, p := range in.Peers {
		if p.IsIBGP() {
			continue
		}

		// A peer using a private ASN, filtered by a policy that rejects private
		// ASNs in the path, loses every route it sends.
		if IsPrivateASN(in.BogonASNs, p.RemoteASN) {
			for _, pol := range p.ImportPolicies {
				if pol.BogonASNs == store.BogonASNsAll {
					add(SeverityDanger, p.Name,
						"AS%d is a private AS number, and %s rejects private ASNs in the AS path — this session will reject every route it receives. Use a policy with \"all except private\" instead.",
						p.RemoteASN, pol.Name)
				}
			}
		}

		// Import policies are ANDed, so the first policy to reject the default
		// route makes any later "accept the default" unreachable.
		var rejectsDefault string
		for _, pol := range p.ImportPolicies {
			if pol.DefaultRoute == store.DefaultReject && rejectsDefault == "" {
				rejectsDefault = pol.Name
			}
		}
		if rejectsDefault != "" {
			for _, pol := range p.ImportPolicies {
				switch pol.DefaultRoute {
				case store.DefaultAccept:
					add(SeverityDanger, p.Name,
						"%s rejects the default route, so %s can never accept it. Import policies are applied in order and any one of them can veto a route.",
						rejectsDefault, pol.Name)
				case store.DefaultOnly:
					add(SeverityDanger, p.Name,
						"%s rejects the default route and %s rejects everything else, so this session will accept nothing at all.",
						rejectsDefault, pol.Name)
				}
			}
		}

		// Origin filters are for peers you provide transit to. Applied to an
		// upstream — who originates almost nothing and relays everything — they
		// reject essentially the whole internet.
		if p.Role == store.RoleUpstream {
			if p.OriginPeerOnly {
				add(SeverityDanger, p.Name,
					"\"Only prefixes this peer originates\" is set on an upstream. An upstream relays the internet rather than originating it, so this rejects nearly every route it sends.")
			}
			for _, pol := range p.ImportPolicies {
				if pol.OriginASSetID.Valid {
					add(SeverityDanger, p.Name,
						"%s filters origins through an AS set, but this peer's role is upstream. Origin filtering belongs on customers, not on the provider you buy transit from.",
						pol.Name)
				}
			}
		}

		// "Only what this peer originates" and "only origins in this AS set"
		// compose with AND, so an AS set that omits the peer's own ASN rejects
		// everything.
		if p.OriginPeerOnly {
			for _, pol := range p.ImportPolicies {
				if !pol.OriginASSetID.Valid {
					continue
				}
				as, ok := asSets[pol.OriginASSetID.Int64]
				if ok && !as.Contains(p.RemoteASN) {
					add(SeverityDanger, p.Name,
						"AS%d is not a member of %s, and both origin filters must pass — this session will accept nothing. Add the peer's own ASN to the set, or drop one of the two filters.",
						p.RemoteASN, as.Name)
				}
			}
		}

		if len(p.ImportPolicies) == 0 {
			add(SeverityWarn, p.Name,
				"No import policy. Everything this peer announces will be accepted, including bogons and routes with your own ASN in the path.")
		}

		// EXPORT_OWN ships pointing at the empty ANNOUNCE_V4/V6 sets, so an
		// operator can attach it and quietly announce nothing at all. A set that
		// exists but holds no prefixes permits exactly as much as no set.
		for _, pol := range p.ExportPolicies {
			var withEntries int
			for _, id := range pol.SetIDs {
				if ps, ok := prefixSets[id]; ok && len(ps.Entries) > 0 {
					withEntries++
				}
			}
			if !pol.AnnounceEverything && !pol.AnnounceDefault && !pol.AnnounceFromUpstream &&
				!pol.AnnounceFromIX && !pol.AnnounceFromCustomer && withEntries == 0 {
				detail := "Attach a prefix set to it, or tick one of its route sources."
				if len(pol.SetIDs) > 0 {
					detail = "Its prefix sets are empty — add your aggregates to them."
				}
				add(SeverityDanger, p.Name,
					"%s permits nothing, so this session announces no routes at all. %s", pol.Name, detail)
			}
		}

		// Announcing the full table to an upstream or an IX peer is a route
		// leak: you become transit for the whole internet.
		if p.Role == store.RoleUpstream || p.Role == store.RoleIXPeer {
			for _, pol := range p.ExportPolicies {
				if pol.AnnounceEverything {
					add(SeverityDanger, p.Name,
						"%s announces the full table to a peer whose role is %q. That is a route leak — you would be offering transit to everyone. Announce only your own prefixes and your customers'.",
						pol.Name, p.Role)
				}
				if pol.AnnounceFromUpstream && p.Role == store.RoleUpstream {
					add(SeverityDanger, p.Name,
						"%s re-announces routes learned from upstreams back to an upstream. That is a route leak.", pol.Name)
				}
			}
		}
	}

	// RPKI findings are about the model as a whole, not one peer.
	var rtrEnabled bool
	for _, srv := range in.RPKIServers {
		if srv.Enabled {
			rtrEnabled = true
		}
	}
	var validating, logging []string
	for _, pol := range in.Policies {
		switch pol.ROV {
		case store.ROVReject:
			validating = append(validating, pol.Name)
		case store.ROVLog:
			logging = append(logging, pol.Name)
		}
	}
	switch {
	case rtrEnabled && len(validating) == 0 && len(logging) == 0:
		add(SeverityWarn, "",
			"An RTR server is configured but no import policy validates against it. RPKI data is being fetched and ignored.")
	case len(logging) > 0 && len(validating) == 0:
		add(SeverityWarn, "",
			"RPKI is in log-only mode everywhere (%s). Invalid routes are tagged with RPKI_INVALID and still used. Switch a policy to \"reject invalid\" once you have counted them.",
			strings.Join(logging, ", "))
	}

	if in.LocalASN == 0 || in.RouterID == "" {
		add(SeverityWarn, "", "Set the router ID and local ASN before applying anything.")
	}
	return out
}
