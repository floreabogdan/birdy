package render

import "regexp"

// passwordLine matches a BIRD `password "..."` option, capturing everything
// around the quoted secret. BIRD string literals have no escape sequences, so
// the value is simply "everything up to the next double quote".
var passwordLine = regexp.MustCompile(`(?m)^(\s*password\s+)"[^"]*"`)

// MaskPasswords replaces every BGP session password in a bird.conf with
// MaskedPassword.
//
// It exists so the Changes screen can diff the running config against a
// candidate without the running config's secrets ever reaching the browser —
// and so an unchanged password does not show up as a change on every render.
// The cost is that a password *value* change is invisible in the diff.
func MaskPasswords(cfg string) string {
	return passwordLine.ReplaceAllString(cfg, `${1}"`+MaskedPassword+`"`)
}
