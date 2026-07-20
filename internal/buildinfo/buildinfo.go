// Package buildinfo holds birdy's version string.
package buildinfo

// Version is the build version. The release build overrides it with the git
// tag via -ldflags "-X github.com/floreabogdan/birdy/internal/buildinfo.Version=...".
// A source build leaves it at this default.
var Version = "0.4.2-dev"

// Commit identifies the exact source revision for development-channel update
// checks. Release builds may leave it empty because their version tag is enough.
var Commit = "unknown"
