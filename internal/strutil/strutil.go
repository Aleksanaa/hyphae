// Package strutil holds small, dependency-free string helpers shared across all
// layers (it imports only the standard library).
package strutil

// Truncate shortens s to at most max runes, replacing the trailing overflow with
// a single "…" that counts toward max. It is rune-aware — it never splits a
// multi-byte rune. s is returned unchanged when it already fits; max < 1 yields
// the empty string.
func Truncate(s string, max int) string {
	if max < 1 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
