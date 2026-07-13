package birdc

// Raw byte captures from a live BIRD 2.17.1 control socket, taken with a raw
// unix-socket probe so the exact wire framing (code/dash/space) is preserved
// verbatim — birdc itself strips this before printing, so these were captured
// below that layer.
//
// Addresses and AS numbers have been rewritten to the documentation ranges of
// RFC 5398 (AS64496, AS65551) and RFC 5737 / RFC 3849 (192.0.2.0/24,
// 198.51.100.0/24, 203.0.113.0/24, 2001:db8::/32). The framing is untouched.

const fixtureBanner = "0001 BIRD 2.17.1 ready.\n"

const fixtureShowStatus = "1000-BIRD 2.17.1\n" +
	"1011-Router ID is 203.0.113.58\n" +
	" Hostname is rtr1.example.net\n" +
	" Current server time is 2026-07-10 09:56:01.268\n" +
	" Last reboot on 2026-07-08 14:39:57.632\n" +
	" Last reconfiguration on 2026-07-08 18:53:57.362\n" +
	"0013 Daemon is up and running\n"

const fixtureShowProtocols = "2002-Name       Proto      Table      State  Since         Info\n" +
	"1002-anchors6   Static     master6    up     2026-07-08    \n" +
	" anchors4   Static     master4    up     2026-07-08    \n" +
	" device1    Device     ---        up     2026-07-08    \n" +
	" direct1    Direct     ---        up     2026-07-08    \n" +
	" k_ipv4     Kernel     master4    up     2026-07-08    \n" +
	" k_ipv6     Kernel     master6    up     2026-07-08    \n" +
	" edge_v4    BGP        ---        up     2026-07-08    Established   \n" +
	" edge_v6    BGP        ---        up     2026-07-08    Established   \n" +
	"0000 \n"

// Real output from a birdy-managed router: names longer than the column
// (originate_ANNOUNCE_V4/V6) overflow and shift the later fields right, and the
// Since is a bare time. The fixed-column parser miscounted these as down.
const fixtureShowProtocolsLongNames = "2002-Name       Proto      Table      State  Since         Info\n" +
	"1002- device1    Device     ---        up     2026-07-08\n" +
	" originate_ANNOUNCE_V4 Static     master4    up     09:01:15.741\n" +
	" originate_ANNOUNCE_V6 Static     master6    up     09:01:15.741\n" +
	" cloudflare RPKI       ---        up     09:04:16.870  Established\n" +
	" edge_v4     BGP        ---        up     09:01:17.983  Established\n" +
	"0000 \n"

const fixtureShowProtocolsAllBGP = "2002-Name       Proto      Table      State  Since         Info\n" +
	"1002-edge_v4    BGP        ---        up     2026-07-08    Established   \n" +
	"1006-  BGP state:          Established\n" +
	"     Neighbor address: 203.0.113.57\n" +
	"     Neighbor AS:      64496\n" +
	"     Local AS:         65551\n" +
	"     Neighbor ID:      198.51.100.1\n" +
	"     Local capabilities\n" +
	"       Multiprotocol\n" +
	"         AF announced: ipv4\n" +
	"       Route refresh\n" +
	"       Graceful restart\n" +
	"       4-octet AS numbers\n" +
	"       Enhanced refresh\n" +
	"       Long-lived graceful restart\n" +
	"     Neighbor capabilities\n" +
	"       Multiprotocol\n" +
	"         AF announced: ipv4\n" +
	"       Route refresh\n" +
	"       Graceful restart\n" +
	"         Restart time: 120\n" +
	"         AF supported: ipv4\n" +
	"         AF preserved: ipv4\n" +
	"       4-octet AS numbers\n" +
	"       Long-lived graceful restart\n" +
	"     Session:          external multihop AS4\n" +
	"     Source address:   203.0.113.58\n" +
	"     Hold timer:       50.850/90\n" +
	"     Keepalive timer:  11.488/30\n" +
	"     Send hold timer:  154.431/180\n" +
	"   Channel ipv4\n" +
	"     State:          UP\n" +
	"     Table:          master4\n" +
	"     Preference:     100\n" +
	"     Input filter:   import_v4\n" +
	"     Output filter:  export_v4\n" +
	"     Import limit:   2000000\n" +
	"       Action:       disable\n" +
	"     Routes:         1 imported, 1 exported, 1 preferred\n" +
	"     Route change stats:     received   rejected   filtered    ignored   accepted\n" +
	"       Import updates:              1          0          0          0          1\n" +
	"       Import withdraws:            0          0        ---          0          0\n" +
	"       Export updates:              5          2          2        ---          1\n" +
	"       Export withdraws:            0        ---        ---        ---          0\n" +
	"     BGP Next hop:   203.0.113.58\n" +
	"     IGP IPv4 table: master4\n" +
	" \n" +
	"0000 \n"

const fixtureShowProtocolsAllDevice = "2002-Name       Proto      Table      State  Since         Info\n" +
	"1002-device1    Device     ---        up     2026-07-08    \n" +
	"1006-\n" +
	"0000 \n"

const fixtureShowRouteCount = "1007-5 of 5 routes for 4 networks in table master4\n" +
	" 5 of 5 routes for 4 networks in table master6\n" +
	"0014 Total: 10 of 10 routes for 8 networks in 2 tables\n"

const fixtureShowRoute = "1007-Table master4:\n" +
	" 0.0.0.0/0            unicast [edge_v4 2026-07-08] * (100) [AS64496i]\n" +
	" \tvia 203.0.113.57 on eno1\n" +
	" 203.0.113.56/30       unicast [direct1 2026-07-08] * (240)\n" +
	" \tdev eno1\n" +
	" 192.0.2.0/24      unicast [direct1 2026-07-08] * (240)\n" +
	" \tdev eno1\n" +
	"                      unreachable [anchors4 2026-07-08] (200)\n" +
	" 192.168.10.0/24      unicast [direct1 2026-07-08] * (240)\n" +
	" \tdev home\n" +
	" \n" +
	" Table master6:\n" +
	" ::/0                 unicast [edge_v6 2026-07-08 from 2001:db8:1::1] * (100) [AS64496i]\n" +
	" \tvia fe80::d699:6c00:730b:6180 on eno1\n" +
	" fd00:1234::/64    unicast [direct1 2026-07-08] * (240)\n" +
	" \tdev home\n" +
	" 2001:db8:1::/126 unicast [direct1 2026-07-08] * (240)\n" +
	" \tdev eno1\n" +
	" 2001:db8:100::/40  unicast [direct1 2026-07-08] * (240)\n" +
	" \tdev eno1\n" +
	"                      unreachable [anchors6 2026-07-08] (200)\n" +
	"0000 \n"

const fixtureShowRouteFor = "1007-Table master4:\n" +
	" 192.0.2.0/24      unicast [direct1 2026-07-08] * (240)\n" +
	" \tdev eno1\n" +
	"                      unreachable [anchors4 2026-07-08] (200)\n" +
	"0000 \n"

const fixtureShowRouteAllFor = "1007-Table master4:\n" +
	" 192.0.2.0/24      unicast [direct1 2026-07-08] * (240)\n" +
	" \tdev eno1\n" +
	"1008-\tType: device univ\n" +
	"1007-                     unreachable [anchors4 2026-07-08] (200)\n" +
	"1008-\tType: static univ\n" +
	"0000 \n"

// A BGP route as "show route for X all" renders it: a summary line followed by
// the per-route attribute detail block, including standard and large communities.
const fixtureShowRouteAllBGP = "1007-Table master4:\n" +
	" 203.0.113.0/24     unicast [edge_v4 2026-07-08] * (100) [AS64496i]\n" +
	" \tvia 203.0.113.57 on eno1\n" +
	"1008-\tType: BGP univ\n" +
	"1008-\tBGP.origin: IGP\n" +
	"1008-\tBGP.as_path: 64496\n" +
	"1008-\tBGP.next_hop: 203.0.113.57\n" +
	"1008-\tBGP.local_pref: 100\n" +
	"1008-\tBGP.community: (65000,100) (65535,666)\n" +
	"1008-\tBGP.large_community: (64496, 1, 1000) (64496, 2, 1)\n" +
	"1008-\tBGP.med: 0\n" +
	"0000 \n"

const fixtureShowRouteExport = "1007-Table master4:\n" +
	" 192.0.2.0/24      unicast [direct1 2026-07-08] * (240)\n" +
	" \tdev eno1\n" +
	"0000 \n"

const fixtureShowRouteProtocol = "1007-Table master4:\n" +
	" 0.0.0.0/0            unicast [edge_v4 2026-07-08] * (100) [AS64496i]\n" +
	" \tvia 203.0.113.57 on eno1\n" +
	"0000 \n"

const fixtureShowRouteNoExport = "1007-Table master4:\n" +
	" 203.0.113.56/30       unicast [direct1 2026-07-08] * (240)\n" +
	" \tdev eno1\n" +
	" 192.168.10.0/24      unicast [direct1 2026-07-08] * (240)\n" +
	" \tdev home\n" +
	"0000 \n"

const fixtureErrorSyntax = "9001 syntax error, unexpected CF_SYM_UNDEFINED\n"
