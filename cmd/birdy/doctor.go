package main

import (
	"flag"
	"fmt"

	"github.com/floreabogdan/birdy/internal/doctor"
)

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	socketPath := fs.String("socket", defaultSocketPath, "BIRD control socket path")
	configDir := fs.String("config-dir", defaultConfigDir, "BIRD config directory")
	birdConf := fs.String("bird-conf", defaultConfigDir+"/bird.conf", "the bird.conf birdy reads and (unless read-only) writes")
	birdBinary := fs.String("bird-binary", defaultBirdBinary, "bird binary name or path")
	systemdUnit := fs.String("systemd-unit", defaultSystemdUnit, "systemd unit name BIRD runs under")
	dbPath := fs.String("db", defaultDBPath, "path to birdy's SQLite database")
	fs.Parse(args)

	results := doctor.Run(doctor.Config{
		SocketPath:   *socketPath,
		ConfigDir:    *configDir,
		BirdConfPath: *birdConf,
		BirdBinary:   *birdBinary,
		SystemdUnit:  *systemdUnit,
		DBPath:       *dbPath,
	})

	for _, r := range results {
		fmt.Printf("[%-4s] %-18s %s\n", r.Status, r.Name, r.Detail)
	}

	if doctor.Failed(results) {
		fmt.Println("\nOne or more checks failed.")
		return fmt.Errorf("preflight checks failed")
	}
	fmt.Println("\nAll checks passed (or are informational warnings).")
	return nil
}
