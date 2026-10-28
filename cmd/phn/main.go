// Command phn is the Phaenon compiler.
package main

import (
	"fmt"
	"os"

	"github.com/phaenonlang/phaenon/internal/lexer"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "lex":
		lex(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: phn lex [--php] <file>")
	os.Exit(2)
}

// lex prints the token stream of a file — a debugging aid while the parser
// does not exist yet.
func lex(args []string) {
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
	for _, t := range lexer.Tokenize(src, mode) {
		fmt.Printf("%d\t%s\t%q\n", t.Line, t.Kind, t.Text(src))
	}
}
