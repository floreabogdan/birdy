//go:build !unix

package main

// adoptDBOwnership is a Unix concern: there is no service user to hand the
// database to on other platforms.
func adoptDBOwnership(string) {}
