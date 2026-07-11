//go:build !unix

package doctor

// checkConfigReadable is a no-op off Unix: file ownership and process uid/gid do
// not translate. birdy only ever writes a config on a Unix router, so this never
// runs where it matters.
func checkConfigReadable(cfg Config) Result {
	return Result{"config readable by BIRD", Warn, "ownership check is only available on Unix hosts"}
}
