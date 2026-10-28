// Command phn is the Phaenon compiler.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/phaenonlang/phaenon/internal/emit"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/migrate"
	"github.com/phaenonlang/phaenon/internal/parser"
	"github.com/phaenonlang/phaenon/internal/printer"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "build":
		build(os.Args[2:])
	case "migrate":
		migrateCmd(os.Args[2:])
	case "lex":
		lex(os.Args[2:])
	case "print":
		printCmd(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, strings.TrimSpace(`
usage:
  phn build [--out DIR] [--check] <path>...   compile .phn files to .php (directories are walked)
  phn migrate <path>...                       rewrite .php files as .phn (strips <?php and declare(strict_types=1))
  phn lex   [--php] <file>                    dump the token stream
  phn print [--php] <file>                    parse and pretty-print (round-trip debugging aid)

build writes Foo.php next to Foo.phn, or under DIR (mirroring the path given) with --out.
--check writes nothing and fails when any output is missing or stale.
--php reads the file as plain PHP (with <?php tags) instead of Phaenon.`))
	os.Exit(2)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// collect expands paths to the files with the given extension, walking directories.
func collect(paths []string, ext string) []string {
	var files []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			fail("%v", err)
		}
		if !info.IsDir() {
			files = append(files, p)
			continue
		}
		filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				fail("%v", err)
			}
			if d.IsDir() && d.Name() == "vendor" && path != p {
				return filepath.SkipDir
			}
			if !d.IsDir() && filepath.Ext(path) == ext {
				files = append(files, path)
			}
			return nil
		})
	}
	return files
}

// ------------------------------------------------------------------ build

func build(args []string) {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	out := fs.String("out", "", "write outputs under this directory instead of beside the sources")
	check := fs.Bool("check", false, "verify outputs are up to date; write nothing")
	fs.Parse(args)
	if fs.NArg() == 0 {
		usage()
	}
	files := collect(fs.Args(), ".phn")
	failed, written, stale := 0, 0, 0
	for _, in := range files {
		src, err := os.ReadFile(in)
		if err != nil {
			fail("%v", err)
		}
		php, err := emit.File(src, emit.Options{SourceName: in, StrictTypes: true})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			failed++
			continue
		}
		dst := strings.TrimSuffix(in, ".phn") + ".php"
		if *out != "" {
			dst = filepath.Join(*out, dst)
		}
		cur, readErr := os.ReadFile(dst)
		upToDate := readErr == nil && bytes.Equal(cur, php)
		if upToDate {
			continue
		}
		if *check {
			fmt.Fprintf(os.Stderr, "%s: stale or missing\n", dst)
			stale++
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			fail("%v", err)
		}
		if err := os.WriteFile(dst, php, 0o644); err != nil {
			fail("%v", err)
		}
		written++
	}
	switch {
	case failed > 0:
		fail("%d of %d files failed to compile", failed, len(files))
	case *check && stale > 0:
		fail("%d of %d outputs are stale; run `phn build`", stale, len(files))
	case *check:
		fmt.Printf("%d files up to date\n", len(files))
	default:
		fmt.Printf("%d files compiled, %d written\n", len(files), written)
	}
}

// ------------------------------------------------------------------ migrate

func migrateCmd(args []string) {
	if len(args) == 0 {
		usage()
	}
	files := collect(args, ".php")
	skipped, migrated := 0, 0
	for _, in := range files {
		src, err := os.ReadFile(in)
		if err != nil {
			fail("%v", err)
		}
		phn, err := migrate.File(src, migrate.Options{})
		if err != nil {
			if errors.Is(err, migrate.ErrNotPlainCode) || errors.Is(err, migrate.ErrGenerated) {
				fmt.Fprintf(os.Stderr, "%s: skipped: %v\n", in, err)
				skipped++
				continue
			}
			fail("%s: %v", in, err)
		}
		dst := strings.TrimSuffix(in, ".php") + ".phn"
		if err := os.WriteFile(dst, phn, 0o644); err != nil {
			fail("%v", err)
		}
		if err := os.Remove(in); err != nil {
			fail("%v", err)
		}
		migrated++
	}
	fmt.Printf("%d files migrated, %d skipped\n", migrated, skipped)
}

// ------------------------------------------------------------------ lex / print

// modeAndFile parses the shared [--php] <file> arguments.
func modeAndFile(args []string) (lexer.Mode, []byte) {
	mode := lexer.ModePhaenon
	if len(args) > 0 && args[0] == "--php" {
		mode = lexer.ModePHP
		args = args[1:]
	}
	if len(args) != 1 {
		usage()
	}
	src, err := os.ReadFile(args[0])
	if err != nil {
		fail("%v", err)
	}
	return mode, src
}

// lex prints the token stream of a file.
func lex(args []string) {
	mode, src := modeAndFile(args)
	for _, t := range lexer.Tokenize(src, mode) {
		fmt.Printf("%d\t%s\t%q\n", t.Line, t.Kind, t.Text(src))
	}
}

// printCmd parses a file and writes the canonical pretty-printed form.
func printCmd(args []string) {
	mode, src := modeAndFile(args)
	f, err := parser.Parse(src, mode)
	if err != nil {
		fail("%v", err)
	}
	os.Stdout.WriteString(printer.File(f))
}
