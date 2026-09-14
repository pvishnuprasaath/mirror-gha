# Provenance

This package (`ghaexpr`) is a surgical extraction of the expression
lexer/parser from [rhysd/actionlint](https://github.com/rhysd/actionlint),
pinned at commit
[`011a6d1`](https://github.com/rhysd/actionlint/commit/011a6d15e749bb3f2d771eed9c7aa0e7e3e10ee7).

Files copied unmodified except for the package rename
(`actionlint` → `ghaexpr`):

| This file | Source file |
|---|---|
| `ast.go` | `expr_ast.go` |
| `lexer.go` | `expr_lexer.go` |
| `parser.go` | `expr_parser.go` |
| `error.go` | `expr.go` |
| `quotes.go` | `quotes.go` (a small parser-error formatting helper `expr_parser.go` depends on) |

## Why an extraction instead of vendoring the full module

actionlint's root Go package pulls in ~10 transitive dependencies
(`doublestar`, `fatih/color`, `go-shellwords`, `robfig/cron`, YAML parsing,
etc.) used by its linting features — none of which the expression
lexer/parser needs. These four files import only the Go standard library
(`fmt`, `strconv`, `strings`, `text/scanner`), so copying just them avoids
dragging in an unrelated dependency tree, at the cost of needing to
manually re-sync if actionlint's expression grammar changes upstream.

`mirror-gha`'s own `internal/engine/expr.go` interprets the `ExprNode`
tree this package produces against our `Context` type — this package only
lexes and parses; it does not evaluate.

## License

MIT, see [`LICENSE`](LICENSE). Copyright (c) 2021 rhysd.
