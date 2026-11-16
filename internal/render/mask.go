package render

import "strings"

// MaskPasswords replaces every password in a bird.conf with MaskedPassword:
// the string literal after each `password` keyword, wherever it sits — the raw
// block is free text, and BIRD does not care whether an option starts a line.
//
// It exists so the Changes screen can diff the running config against a
// candidate without the running config's secrets ever reaching the browser —
// and so an unchanged password does not show up as a change on every render.
// The cost is that a password *value* change is invisible in the diff.
//
// It reads the text the way BIRD's lexer does, rather than matching a pattern,
// because a pattern cannot tell the keyword from the same word inside a string
// or a comment. Strings run to the next double quote and never past a line
// end (BIRD has no escapes and refuses a multi-line string); comments are `#`
// to the end of the line and `/* … */`. Comments are left as they are: the
// renderer writes free-text descriptions into `#` comments unmasked, and a
// masked live file must stay byte-identical to a masked render.
func MaskPasswords(cfg string) string {
	var b strings.Builder
	b.Grow(len(cfg))
	// afterKeyword is true while the last token was `password`, so the next
	// token, if it is a string, is the secret. Whitespace and comments do not
	// end it; BIRD skips both between the two.
	afterKeyword := false
	for i := 0; i < len(cfg); {
		c := cfg[i]
		switch {
		case c == '#':
			end := strings.IndexByte(cfg[i:], '\n')
			if end < 0 {
				end = len(cfg) - i
			}
			b.WriteString(cfg[i : i+end])
			i += end
		case c == '/' && strings.HasPrefix(cfg[i:], "/*"):
			end := strings.Index(cfg[i+2:], "*/")
			if end < 0 {
				end = len(cfg) - i
			} else {
				end += 4
			}
			b.WriteString(cfg[i : i+end])
			i += end
		case c == '"':
			end := strings.IndexAny(cfg[i+1:], "\"\n")
			closed := end >= 0 && cfg[i+1+end] == '"'
			if end < 0 {
				end = len(cfg) - i - 1
			}
			// An unterminated secret is masked too: BIRD would refuse the file,
			// but the text can still be shown.
			if afterKeyword {
				b.WriteString(`"` + MaskedPassword)
				if closed {
					b.WriteByte('"')
				}
			} else {
				b.WriteString(cfg[i : i+1+end])
				if closed {
					b.WriteByte('"')
				}
			}
			i += 1 + end
			if closed {
				i++
			}
			afterKeyword = false
		case isSymbolStart(c):
			end := i + 1
			for end < len(cfg) && isSymbolChar(cfg[end]) {
				end++
			}
			afterKeyword = cfg[i:end] == "password"
			b.WriteString(cfg[i:end])
			i = end
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			b.WriteByte(c)
			i++
		default:
			afterKeyword = false
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

func isSymbolStart(c byte) bool {
	return c == '_' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

func isSymbolChar(c byte) bool { return isSymbolStart(c) || ('0' <= c && c <= '9') }
