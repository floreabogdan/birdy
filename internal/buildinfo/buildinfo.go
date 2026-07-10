// Package buildinfo holds birdy's version string.
package buildinfo

// Version is a plain constant for now; overriding via -ldflags isn't worth
// the added build complexity until birdy has real releases to tag.
const Version = "0.1.0-m1"
