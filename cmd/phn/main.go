// Command phn is the Phaenon compiler.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
	"github.com/phaenonlang/phaenon/internal/printer"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
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
  phn lex   [--php] <file>   dump the token stream
  phn print [--php] <file>   parse and pretty-print (round-trip debugging aid)

--php reads the file as plain PHP (with <?php tags) instead of Phaenon.`))
	os.Exit(2)
}

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
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
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
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Stdout.WriteString(printer.File(f))
}
