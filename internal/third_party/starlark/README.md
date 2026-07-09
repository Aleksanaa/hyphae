# Vendored go.starlark.net

Vendored from https://github.com/google/starlark-go.

## Local modifications

**c004cc16 — add repl-like result print to starlark**
Top-level expression statements now emit a `PRINT_EXPR` opcode (instead of `POP`) when `FileOptions.REPL` is true. The interpreter handles `PRINT_EXPR` by calling `thread.Print` for any non-None value. Changes are in:
- `syntax/options.go` — `REPL bool` field on `FileOptions`
- `internal/compile/compile.go` — `PRINT_EXPR` opcode, `repl`/`topLevel` fields on `pcomp`/`fcomp`
- `starlark/interp.go` — `PRINT_EXPR` case in the bytecode interpreter

When updating from upstream, re-apply these changes.
