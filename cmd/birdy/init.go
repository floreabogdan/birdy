package main

import (
	"bufio"
	"database/sql"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"strings"

	"github.com/floreabogdan/birdy/internal/snapshot"
	"github.com/floreabogdan/birdy/internal/store"
	"github.com/floreabogdan/birdy/internal/web"
)

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	dbPath := fs.String("db", defaultDBPath, "path to birdy's SQLite database")
	socketPath := fs.String("socket", defaultSocketPath, "BIRD control socket path")
	listen := fs.String("listen", defaultListen, "address for the web UI to listen on")
	label := fs.String("label", "", "friendly name for this router (e.g. its hostname)")
	asn := fs.Int64("asn", 0, "local AS number (used by the config renderer)")
	routerID := fs.String("router-id", "", "BGP router ID, as an IPv4 address (used by the config renderer; settable later in the UI)")
	username := fs.String("username", "admin", "admin username")
	password := fs.String("password", "", "admin password (if omitted, you'll be prompted — preferred, since flags can end up in shell history)")
	fs.Parse(args)

	if err := snapshot.ApplyPendingRestore(*dbPath, nil); err != nil {
		return fmt.Errorf("apply pending restore: %w", err)
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer st.Close()

	hasUser, err := st.HasAnyUser()
	if err != nil {
		return err
	}
	if hasUser {
		return fmt.Errorf("birdy is already initialized at %s (a user account already exists)", *dbPath)
	}

	pw := *password
	if pw == "" {
		pw, err = promptPassword()
		if err != nil {
			return err
		}
	}
	if len(pw) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}

	hash, err := web.HashPassword(pw)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	if _, err := st.CreateUser(*username, hash); err != nil {
		return fmt.Errorf("create user: %w", err)
	}

	if *routerID != "" {
		if addr, err := netip.ParseAddr(*routerID); err != nil || !addr.Is4() {
			return fmt.Errorf("router id %q must be an IPv4 address", *routerID)
		}
	}
	settings := store.Settings{
		RouterLabel:    *label,
		RouterID:       *routerID,
		BirdSocketPath: *socketPath,
		ListenAddr:     *listen,
	}
	if *asn > 0 {
		settings.LocalASN = sql.NullInt64{Int64: *asn, Valid: true}
	}
	if err := st.SaveSettings(settings); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}

	fmt.Printf("birdy initialized at %s\n", *dbPath)
	fmt.Printf("  admin user:     %s\n", *username)
	fmt.Printf("  BIRD socket:    %s\n", *socketPath)
	fmt.Printf("  listen address: %s\n", *listen)
	fmt.Println("\nNext: run \"birdy doctor\" to check BIRD connectivity, then \"birdy server\".")
	return nil
}

func promptPassword() (string, error) {
	fmt.Print("Admin password (not hidden — run in a private session): ")
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	pw := strings.TrimRight(line, "\r\n")
	fmt.Print("Confirm password: ")
	line2, err := r.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read password confirmation: %w", err)
	}
	if strings.TrimRight(line2, "\r\n") != pw {
		return "", fmt.Errorf("passwords did not match")
	}
	return pw, nil
}
