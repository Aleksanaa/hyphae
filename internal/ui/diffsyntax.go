// Syntax highlighting adapted from differ (https://github.com/jansmrcka/differ)
// by Jan Smrčka — MIT License (Copyright (c) 2026 Jan Smrčka).
package ui

import (
	"path/filepath"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	chromastyles "github.com/alecthomas/chroma/v2/styles"
	"github.com/gdamore/tcell/v2"
	"github.com/lrstanley/bubbletint/chromatint/v2"
)

var (
	diffLexerCache  sync.Map
	diffChromaStyle *chroma.Style
)

// rebuildChromaStyle derives the chroma style from the active bubbletint tint so
// syntax highlighting matches the rest of the UI palette. Called by applyTint
// (theme.go) whenever the theme changes.
func rebuildChromaStyle() {
	if s, err := chroma.NewStyle("hyphae-tint", chromatint.StyleEntry(ActiveTint, false)); err == nil {
		diffChromaStyle = s
		return
	}
	diffChromaStyle = chromastyles.Get("dracula")
	if diffChromaStyle == nil {
		diffChromaStyle = chromastyles.Get("monokai")
	}
}

func getDiffLexer(filename string) chroma.Lexer {
	ext := filepath.Ext(filename)
	if ext == "" {
		ext = filepath.Base(filename)
	}
	if v, ok := diffLexerCache.Load(ext); ok {
		return v.(chroma.Lexer)
	}
	l := lexers.Match(filename)
	if l == nil {
		l = lexers.Fallback
	}
	l = chroma.Coalesce(l)
	diffLexerCache.Store(ext, l)
	return l
}

func getMarkdownLexer(lang string) chroma.Lexer {
	key := "lang:" + lang
	if v, ok := diffLexerCache.Load(key); ok {
		return v.(chroma.Lexer)
	}
	l := lexers.Get(lang)
	if l == nil {
		l = lexers.Fallback
	}
	l = chroma.Coalesce(l)
	diffLexerCache.Store(key, l)
	return l
}

// tokenizeForMarkdown returns (rune, style) pairs for content using chroma highlighting.
// No background is set; tokens with no chroma color fall back to Theme.CodeColor.
func tokenizeForMarkdown(content, lang string) []diffStyledRune {
	plain := func() []diffStyledRune {
		rs := []rune(content)
		out := make([]diffStyledRune, len(rs))
		for i, r := range rs {
			out[i] = diffStyledRune{R: r, Style: tcell.StyleDefault}
		}
		return out
	}
	if diffChromaStyle == nil || content == "" || lang == "" {
		return plain()
	}
	l := getMarkdownLexer(lang)
	iter, err := l.Tokenise(nil, content)
	if err != nil {
		return plain()
	}
	var out []diffStyledRune
	for _, tok := range iter.Tokens() {
		entry := diffChromaStyle.Get(tok.Type)
		fg := chromaToTcell(entry, Theme.CodeColor)
		st := tcell.StyleDefault.Foreground(fg)
		for _, r := range []rune(tok.Value) {
			out = append(out, diffStyledRune{R: r, Style: st})
		}
	}
	return out
}

// diffStyledRune is a rune paired with a tcell style.
type diffStyledRune struct {
	R     rune
	Style tcell.Style
}

// tokenizeForTcell returns (rune, style) pairs for content using chroma highlighting.
// bg is applied as the background for every rune.
func tokenizeForTcell(content, filename string, bg tcell.Color) []diffStyledRune {
	plain := func() []diffStyledRune {
		rs := []rune(content)
		out := make([]diffStyledRune, len(rs))
		st := tcell.StyleDefault.Background(bg)
		for i, r := range rs {
			out[i] = diffStyledRune{R: r, Style: st}
		}
		return out
	}
	if diffChromaStyle == nil || content == "" {
		return plain()
	}
	l := getDiffLexer(filename)
	iter, err := l.Tokenise(nil, content)
	if err != nil {
		return plain()
	}
	var out []diffStyledRune
	for _, tok := range iter.Tokens() {
		entry := diffChromaStyle.Get(tok.Type)
		fg := chromaToTcell(entry, Theme.Text)
		st := tcell.StyleDefault.Foreground(fg).Background(bg)
		for _, r := range []rune(tok.Value) {
			out = append(out, diffStyledRune{R: r, Style: st})
		}
	}
	return out
}

func chromaToTcell(entry chroma.StyleEntry, fallback tcell.Color) tcell.Color {
	if entry.Colour.IsSet() {
		if c := tcell.GetColor(entry.Colour.String()); c.Valid() {
			return c
		}
	}
	return fallback
}
