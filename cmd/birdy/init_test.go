package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// bcrypt refuses a password over 72 bytes. init must say so in those words
// instead of failing with bcrypt's own "hash password: ..." error.
func TestInitRejectsPasswordOver72Bytes(t *testing.T) {
	db := filepath.Join(t.TempDir(), "birdy.db")
	err := cmdInit([]string{"-db", db, "-password", strings.Repeat("a", 73)})
	if err == nil || !strings.Contains(err.Error(), "at most 72 bytes") {
		t.Fatalf("73-byte password: err = %v, want the 72-byte limit explained", err)
	}
	if strings.Contains(err.Error(), "hash password") {
		t.Fatalf("the limit should be checked before hashing: %v", err)
	}
	if err := cmdInit([]string{"-db", db, "-password", strings.Repeat("a", 72)}); err != nil {
		t.Fatalf("a 72-byte password must be accepted: %v", err)
	}
}
