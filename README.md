# Phaenon

A typed dialect of PHP 8.5+ that compiles to idiomatic PHP — "TypeScript for PHP".

- `phn` — the compiler (this repository, Go)
- `phaenon/runtime` — the PHP runtime package (`\Phaenon\Type`, stdlib)
- `design/` — language design documents

Status: pre-alpha, milestone M0 (parse PHP, print PHP, prove it round-trips against php-src's
`.phpt` corpus).

## Development

```sh
go test ./...
PHAENON_PHP_SRC=../php-src go test ./internal/conformance/... -run Phpt
```

License: MIT.
