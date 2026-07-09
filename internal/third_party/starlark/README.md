# Vendored go.starlark.net

Vendored from https://github.com/google/starlark-go.

## Local modifications

**c004cc16 — add repl-like result print to starlark**
Top-level expression statements now emit a `PRINT_EXPR` opcode (instead of `POP`) when `FileOptions.REPL` is true. The interpreter handles `PRINT_EXPR` by calling `thread.Print` for any non-None value. Changes are in:
- `syntax/options.go` — `REPL bool` field on `FileOptions`
- `internal/compile/compile.go` — `PRINT_EXPR` opcode, `repl`/`topLevel` fields on `pcomp`/`fcomp`
- `starlark/interp.go` — `PRINT_EXPR` case in the bytecode interpreter

When updating from upstream, re-apply these changes.

**4bc42f68 — add f-string literals (f"…{expr}…") to Starlark**
Merged from https://github.com/google/starlark-go/pull/625. Changes are in:
- `syntax/scan.go` — `FSTRING_FULL`, `FSTRING_PART`, `FSTRING_END` tokens; scanner logic
- `syntax/syntax.go` — `FStringExpr` AST node
- `syntax/parse.go` — `FSTRING_*` cases in `parsePrimary()`; adds `"strings"` import
- `syntax/quote.go` — f-string quoting support
- `internal/compile/compile.go` — `FStringExpr` compilation via `.format()` call
- `resolve/resolve.go` — f-string expression resolution

This is an upstream PR not yet merged; verify it landed before dropping this note on update.

**d0c31416 — add \*\* operator, \*\*= augmented assignment, and pow()**
Merged from https://github.com/google/starlark-go/pull/632. Changes are in:
- `syntax/scan.go` — `STARSTAR` token moved between binary ops; `STARSTAR_EQ` added
- `syntax/parse.go` — `parseFactor()` and `parsePower()` for right-associative `**`
- `internal/compile/compile.go` — `STARSTAR` opcode, augmented assignment support
- `starlark/eval.go` — `STARSTAR` case in `Binary()`, `floatPow()` helper
- `starlark/interp.go` — `STARSTAR` added to binop switch
- `starlark/library.go` — `pow()` builtin

This is an upstream PR not yet merged; verify it landed before dropping this note on update.
