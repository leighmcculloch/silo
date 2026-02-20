package ssh

import "strings"

// shellQuote wraps a string in single quotes for safe shell usage,
// escaping any embedded single quotes. If the string contains only
// safe characters, it is returned unquoted.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	// If it looks safe, don't quote.
	safe := true
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '/' || r == '.' || r == '-' || r == '_' || r == '=' || r == ':') {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
