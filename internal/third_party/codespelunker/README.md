# codespelunker

Core logic extracted from [code spelunker (cs)](https://github.com/boyter/cs) by Ben Boyter, MIT licensed.

## Origin

cs is a code-search tool that supports boolean query syntax, ranked results via TF-IDF/BM25, language detection via scc, and gitignore-aware file walking. The full tool includes a TUI (bubbletea), an HTTP server, an MCP server, and a Cobra CLI.

## What was extracted

Only the search pipeline, ranking, and line-extraction layers were kept. Removed:

- **TUI** (`tui.go`, charmbracelet/* dependencies)
- **HTTP server** (`http.go`, HTML templates)
- **MCP server** (`mcp.go`, `github.com/mark3labs/mcp-go`)
- **Result cache** (`cache.go`, `github.com/boyter/simplecache`)
- **CLI** (`main.go`, cobra, pflag)
- **Color/syntax output** (`github.com/fatih/color`, `RenderANSILine`, `RenderHTMLLine`)
- **Console/JSON/vimgrep formatters** (in `console.go`)
- **Snippet extraction for display** (`pkg/snippet/snippet.go` — complex relevance scoring for visual snippets)
- **HTTP-specific file display** (`http.go` file display handler)

### Kept packages

| Package | Origin | Role |
|---------|--------|------|
| `search/` | `cs/pkg/search/` | Query lexer, parser, AST transformer, planner, executor (`EvaluateFile`, `PostEvalMetadataFilters`), term extractor |
| `ranker/` | `cs/pkg/ranker/` | TF-IDF, BM25, structural, and MIN rankers; filename/complexity/test boosts; dedup; declaration classifier |
| `common/` | `cs/pkg/common/` | `FileJob` struct shared by ranker and snippet |
| `snippet/` | `cs/pkg/snippet/snippet_lines.go` | `FindAllMatchingLines`, `FindMatchingLines`, `AddPhraseMatchLocations` |

The main glue file `spelunker.go` is derived from `cs/search.go` (the `DoSearch` function) and `cs/language.go` (language detection via scc), stripped of cache and config complexity.

### Changed import paths

All internal references from `github.com/boyter/cs/v3/pkg/common` were updated to `github.com/aleksanaa/hyphae/internal/third_party/codespelunker/common`. External dependencies (`go-string`, `gocodewalker`, `scc/v3`) are unchanged.

## Dependencies added to hyphae

- `github.com/boyter/go-string` — substring search helpers used by the search executor and ranker
- `github.com/boyter/gocodewalker` — parallel file walker with gitignore/.ignore support
- `github.com/boyter/scc/v3` — language detection and per-byte code/comment/string classification

## Public API

```go
// Search runs a ranked code search in dir.
// Query supports: implicit AND, OR, NOT, "phrases", /regex/, fuzzy~1,
// ext:go, lang:Python, path:pkg, file:test, complexity>=5.
func Search(ctx context.Context, query, dir string, opts Options) ([]*common.FileJob, int, error)
```

Callers rank the returned `[]*common.FileJob` with `ranker.RankResults(...)` and extract lines with `snippet.FindAllMatchingLines(...)`.

## Updating from upstream

When pulling in a newer version of cs:

1. Re-copy the relevant source files from the new `cs/pkg/search/`, `cs/pkg/ranker/`, `cs/pkg/common/`, and `cs/pkg/snippet/snippet_lines.go`.
2. Re-apply the single import path substitution:
   `s|github.com/boyter/cs/v3/pkg/common|github.com/aleksanaa/hyphae/internal/third_party/codespelunker/common|g`
3. Update `spelunker.go` if the `DoSearch` or `fileCodeStats` signatures changed in cs.
4. Run `go mod tidy` and rebuild.
