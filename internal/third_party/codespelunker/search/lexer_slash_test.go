package search

import (
	"strings"
	"testing"
)

func TestScanRegexEscapedSlash(t *testing.T) {
	cases := []struct {
		input   string
		want    string
	}{
		{`/foo\/bar/`, `foo/bar`},
		{`/(https?:\/\/)/`, `(https?://)`},
		{`/simple/`, `simple`},
		{`/\//`, `/`},
	}
	for _, c := range cases {
		l := NewLexer(strings.NewReader(c.input))
		tok := l.scan()
		if tok.Type != REGEX {
			t.Errorf("input %q: expected REGEX token, got %v", c.input, tok.Type)
			continue
		}
		if tok.Literal != c.want {
			t.Errorf("input %q: got literal %q, want %q", c.input, tok.Literal, c.want)
		}
	}
}
