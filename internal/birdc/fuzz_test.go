package birdc

import (
	"bufio"
	"bytes"
	"testing"
)

// FuzzReadFrameAndParse feeds arbitrary bytes through the wire-frame reader and
// then every parser, asserting none of them panic on malformed BIRD output. The
// control socket is the one input birdy does not fully control, and a panic in a
// parser would take down the poller.
func FuzzReadFrameAndParse(f *testing.F) {
	f.Add([]byte("0001 BIRD 2.17.1 ready.\n"))
	f.Add([]byte("2002-Name       Proto      Table      State  Since         Info\n" +
		" edge_v4    BGP        ---        up     2026-07-08    Established\n0000 \n"))
	f.Add([]byte("1000-BIRD 2.17.1\n1013 Daemon is up and running\n"))
	f.Add([]byte("0000 \n"))
	f.Add([]byte("8001 Reply too long\n"))
	f.Add([]byte(""))

	f.Fuzz(func(t *testing.T, data []byte) {
		reply, err := readFrame(bufio.NewReader(bytes.NewReader(data)))
		if err != nil {
			return // a malformed frame is fine to reject; it must just not panic
		}
		// Any frame readFrame accepts must be safe to hand to every parser.
		_, _ = ParseStatus(reply)
		_, _ = ParseProtocols(reply)
		_, _ = ParseProtocolDetail(reply)
		_, _ = ParseRouteCount(reply)
		_, _ = ParseRoutes(reply)
	})
}
