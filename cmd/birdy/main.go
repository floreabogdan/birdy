// Command birdy is a single-router BIRD 2.x manager: a web UI, backed by
// SQLite, that talks to BIRD over its control socket. Run `birdy init` once,
// then `birdy server` (normally under systemd).
package main

import (
	"fmt"
	"os"

	"github.com/floreabogdan/birdy/internal/buildinfo"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	var err error
	switch os.Args[1] {
	case "init":
		err = cmdInit(os.Args[2:])
	case "doctor":
		err = cmdDoctor(os.Args[2:])
	case "server":
		err = cmdServer(os.Args[2:])
	case "version":
		fmt.Println("birdy " + buildinfo.Version)
		return
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "birdy: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "birdy:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `birdy — single-router BIRD 2.x manager

Usage:
  birdy init [flags]     create the database and admin account
  birdy doctor [flags]   run preflight checks against BIRD and the filesystem
  birdy server [flags]   run the web UI and background poller
  birdy version          print the version

Run "birdy <command> -h" for flags on a specific command.
`)
}
