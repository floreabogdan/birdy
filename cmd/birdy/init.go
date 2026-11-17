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

	// Everything that can be checked without the database is checked before it
	// is opened, let alone written: a rejected init must leave nothing behind.
	// The user used to be created before --router-id was validated, so a typo
	// there left a database with an account but no settings — which init then
	// refused to touch ("already initialized") and the server refused to run on.
	if strings.TrimSpace(*username) == "" {
		return fmt.Errorf("username must not be empty")
	}
	if *routerID != "" {
		if addr, err := netip.ParseAddr(*routerID); err != nil || !addr.Is4() {
			return fmt.Errorf("router id %q must be an IPv4 address", *routerID)
		}
	}

	// store.Open refuses a database path that is a symlink: init is typically
	// run as root, in a directory the service account owns.
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
	if len(pw) > web.MaxPasswordBytes {
		return fmt.Errorf("password must be at most %d bytes (bcrypt takes no more); this one is %d", web.MaxPasswordBytes, len(pw))
	}

	hash, err := web.HashPassword(pw)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	// Settings first, the user last. "Initialized" means a user exists (the check
	// above), so if the second write fails the database still counts as
	// uninitialized and running init again completes it: the settings write is
	// an upsert. The other order could strand a user without settings.
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
	if _, err := st.CreateUser(*username, hash); err != nil {
		return fmt.Errorf("create user: %w", err)
	}

	// Close before handing the files over: SQLite still has the -wal open, and the
	// chown has to cover it too.
	st.Close()
	adoptDBOwnership(*dbPath)

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
