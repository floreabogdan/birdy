package notify

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/floreabogdan/birdy/internal/store"
)

// fakeSMTP is a local mailserver that speaks just enough SMTP for net/smtp and
// goes silent, without closing the connection, at stallAt: "greeting" (never
// says hello), "EHLO", or "end-of-data" (takes the whole message, never
// confirms it). That silence is what a wedged or tarpitting server looks like.
func fakeSMTP(t *testing.T, stallAt string) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			go serveFakeSMTP(c, stallAt)
		}
	}()
	return "127.0.0.1", ln.Addr().(*net.TCPAddr).Port
}

func serveFakeSMTP(c net.Conn, stallAt string) {
	if stallAt == "greeting" {
		return // the connection stays open until the test's cleanup closes it
	}
	fmt.Fprint(c, "220 fake ESMTP\r\n")
	r := bufio.NewReader(c)
	inData := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		if inData {
			if line == ".\r\n" {
				if stallAt == "end-of-data" {
					return
				}
				inData = false
				fmt.Fprint(c, "250 queued\r\n")
			}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		cmd := strings.ToUpper(fields[0])
		if cmd == stallAt {
			return
		}
		switch cmd {
		case "EHLO", "HELO":
			fmt.Fprint(c, "250 fake\r\n")
		case "DATA":
			inData = true
			fmt.Fprint(c, "354 go ahead\r\n")
		case "QUIT":
			fmt.Fprint(c, "221 bye\r\n")
			return
		default:
			fmt.Fprint(c, "250 ok\r\n")
		}
	}
}

func emailDest(name, host string, port int) store.Destination {
	return store.Destination{
		Name: name, Type: store.AlertEmail, Enabled: true,
		SMTPHost: host, SMTPPort: port, SMTPSecurity: store.SMTPNone,
		SMTPFrom: "birdy@example.com", SMTPTo: "noc@example.com",
	}
}

// A mailserver that accepts the connection and then never answers must cost a
// bounded wait, wherever in the exchange it goes quiet. Only the dial used to
// have a timeout, so a silent server blocked the sender forever — and with it
// every alert queued behind it.
func TestSMTPStallIsBounded(t *testing.T) {
	for _, stallAt := range []string{"greeting", "EHLO", "end-of-data"} {
		t.Run(stallAt, func(t *testing.T) {
			host, port := fakeSMTP(t, stallAt)
			d := NewDispatcher(openStore(t), nil, 0)
			d.smtpTimeout = 300 * time.Millisecond

			done := make(chan error, 1)
			go func() { done <- d.SendTest(emailDest("mail", host, port)) }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("a server that never answered should fail the send")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("SendTest still blocked after 5s on a silent mailserver")
			}
		})
	}
}

// The control for the stall test: the fake server speaks enough SMTP for a
// whole send, so a stalled send fails because of the stall, not the fake.
func TestSMTPSendCompletes(t *testing.T) {
	host, port := fakeSMTP(t, "")
	if err := NewDispatcher(openStore(t), nil, 0).SendTest(emailDest("mail", host, port)); err != nil {
		t.Fatalf("send to a well-behaved server: %v", err)
	}
}
