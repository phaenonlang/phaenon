# Phaenon

A typed dialect of PHP 8.5+ that compiles to idiomatic PHP — "TypeScript for PHP".

- `phn` — the compiler (this repository, Go)
- `phaenon/runtime` — the PHP runtime package (`\Phaenon\Type`, stdlib)
- `design/` — language design documents

Status: pre-alpha. M0 done — lexer, parser and printer are conformance-tested against php-src's
`.phpt` corpus (token-identical lexing, 100% of PHP-accepted files parse, parse → print round-trips
token-identically, printed files run identically under `php`).

## CLI

```sh
phn lex   [--php] file    # token stream
phn print [--php] file    # parse and pretty-print
```

`--php` reads plain PHP (with `<?php` tags); the default reads Phaenon (code from byte 0).

## Development

```sh
go test ./...                                   # unit tests
PHAENON_PHP_SRC=/abs/path/to/php-src go test ./internal/conformance/   # lexer, parser, printer round trip
PHAENON_EXEC=1 PHAENON_PHP_SRC=/abs/path/to/php-src go test ./internal/conformance/ -run Execution -timeout 30m
```

The execution test runs every `Zend/tests` file and its printed form through `php` and compares
output (≈2.5 min). `PHAENON_PHPT_FILTER=ext/standard/tests` narrows any suite to a corpus subset;
`PHAENON_SHOW_DIFFS=n` controls how many differences are listed.

License: MIT.
