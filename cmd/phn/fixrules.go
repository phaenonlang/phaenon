package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/fix"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
)

// ruleReport names the rule behind each change phn fix reports, for the
// tools whose own report says which rules changed a file
// (design/tooling.md §4): php-cs-fixer's `fix` with `--format=json -v`
// lists the fixers applied to each file, and Rector's `process` with
// `--output-format=json` lists each change's rector with the line of the
// node it rewrote. phn fix asks for that report when the command does not
// choose a format itself, reads it from the tool's standard output, and
// does not print it.
type ruleReport struct {
	tool string // "php-cs-fixer" or "rector"
	// files maps an output's absolute path to the rules the tool applied
	// to it, as the tool names them.
	files map[string][]ruleChange
}

// ruleChange is a rule applied to a file; line, when the tool says, is the
// first line of the node the rule rewrote in the file as the tool read it.
type ruleChange struct {
	rule string
	line int
}

// withRuleReport returns the command with the options that make the tool
// write its report, and whether it does: php-cs-fixer's `fix` and Rector's
// `process` (or Rector without a command), when the command chooses no
// report format of its own.
func withRuleReport(command []string) ([]string, string) {
	tool := toolName(command)
	script := 0
	if phpBinary.MatchString(filepath.Base(command[0])) && len(command) > 1 {
		script = 1
	}
	args := command[script+1:]
	sub := ""
	for _, a := range args {
		if a == "--" {
			break
		}
		if !strings.HasPrefix(a, "-") {
			sub = a
			break
		}
	}
	has := func(opt string) bool {
		for _, a := range args {
			if a == "--" {
				return false
			}
			if a == opt || strings.HasPrefix(a, opt+"=") {
				return true
			}
		}
		return false
	}
	var extra []string
	switch {
	case tool == "php-cs-fixer" && sub == "fix" && !has("--format"):
		extra = []string{"--format=json", "-v"}
	case tool == "rector" && (sub == "process" || sub == "") && !has("--output-format"):
		extra = []string{"--output-format=json"}
	default:
		return command, ""
	}
	// The options go right after the command's name, ahead of any `--`
	// that ends the options.
	at := script + 1
	if sub != "" {
		at = script + 1 + slices.Index(args, sub) + 1
	}
	out := append(append(append([]string(nil), command[:at]...), extra...), command[at:]...)
	return out, tool
}

// readRuleReport parses the tool's standard output. Paths in the report
// are relative to the working directory the tool ran in. A report that
// does not parse names no rules.
func readRuleReport(tool string, stdout []byte) *ruleReport {
	r := &ruleReport{tool: tool, files: map[string][]ruleChange{}}
	add := func(name string, c ruleChange) {
		if abs, err := filepath.Abs(name); err == nil {
			r.files[abs] = append(r.files[abs], c)
		}
	}
	// The JSON is the output's last line; a tool may print a notice first.
	text := bytes.TrimSpace(stdout)
	if i := bytes.LastIndexByte(text, '\n'); i >= 0 && !bytes.HasPrefix(text, []byte("{")) {
		text = text[i+1:]
	}
	switch tool {
	case "php-cs-fixer":
		var doc struct {
			Files []struct {
				Name          string   `json:"name"`
				AppliedFixers []string `json:"appliedFixers"`
			} `json:"files"`
		}
		if json.Unmarshal(text, &doc) != nil {
			return r
		}
		for _, f := range doc.Files {
			for _, fixer := range f.AppliedFixers {
				add(f.Name, ruleChange{rule: fixer})
			}
		}
	case "rector":
		var doc struct {
			FileDiffs []struct {
				File           string   `json:"file"`
				AppliedRectors []string `json:"applied_rectors"`
				Changes        []struct {
					Rector string `json:"rector"`
					Line   int    `json:"line"`
				} `json:"changes"`
			} `json:"file_diffs"`
		}
		if json.Unmarshal(text, &doc) != nil {
			return r
		}
		for _, f := range doc.FileDiffs {
			if len(f.Changes) == 0 {
				for _, rector := range f.AppliedRectors {
					add(f.File, ruleChange{rule: shortRector(rector)})
				}
			}
			for _, c := range f.Changes {
				add(f.File, ruleChange{rule: shortRector(c.Rector), line: c.Line})
			}
		}
	}
	return r
}

// shortRector is a rector's class without its namespace, the name Rector's
// documentation lists it under.
func shortRector(class string) string {
	return class[strings.LastIndexByte(class, '\\')+1:]
}

// rules names the rule behind a change of an output: the one rule applied
// to the file, or, with the lines Rector reports, the rule whose node is
// the innermost to enclose the change; otherwise the file's rules, as
// "one of …". data is the output as the tool read it.
func (r *ruleReport) rules(path string, data []byte, h fix.Hunk) string {
	if r == nil {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	changes := r.files[abs]
	var names []string
	byLine := map[int][]string{}
	for _, c := range changes {
		if !slices.Contains(names, c.rule) {
			names = append(names, c.rule)
		}
		if c.line > 0 && !slices.Contains(byLine[c.line], c.rule) {
			byLine[c.line] = append(byLine[c.line], c.rule)
		}
	}
	switch {
	case len(names) == 0:
		return ""
	case len(names) == 1:
		return names[0]
	}
	if len(byLine) > 0 {
		if found := enclosingRules(data, h, byLine); len(found) == 1 {
			return found[0]
		} else if len(found) > 1 {
			names = found
		}
	}
	sort.Strings(names)
	return "one of " + strings.Join(names, ", ")
}

// enclosingRules returns the rules of the line on which the innermost node
// that encloses the change starts, among the lines the tool reported.
func enclosingRules(data []byte, h fix.Hunk, byLine map[int][]string) []string {
	f, err := parser.Parse(data, lexer.ModePHP)
	if err != nil {
		return nil
	}
	best, bestStart := -1, -1
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		s := n.Span()
		if s.Start > h.Start || h.End > s.End {
			return false // a node that does not enclose the change holds no node that does
		}
		line := bytes.Count(data[:s.Start], []byte("\n")) + 1
		if _, ok := byLine[line]; ok && s.Start >= bestStart {
			best, bestStart = line, s.Start
		}
		return true
	})
	if best < 0 {
		return nil
	}
	return byLine[best]
}

// narrowFixers names the fixer behind each php-cs-fixer change the report
// left at "one of …". It runs php-cs-fixer once per candidate fixer, that
// fixer alone (`--rules`, its default options, the project's configuration
// otherwise), over copies of the outputs those changes are in, and names a
// change by the fixers whose run alone makes exactly that change. A change
// no single fixer makes alone, as when a configured option differs from
// the default, keeps its list.
func narrowFixers(command []string, positions []*fixPosition) {
	var open []*fixPosition
	candidates := map[string]bool{}
	for _, p := range positions {
		if !strings.HasPrefix(p.rule, "one of ") || p.data == nil {
			continue
		}
		open = append(open, p)
		for _, f := range strings.Split(strings.TrimPrefix(p.rule, "one of "), ", ") {
			candidates[f] = true
		}
	}
	if len(open) == 0 {
		return
	}
	prefix, config := fixerPrefix(command)
	dir, err := os.MkdirTemp("", "phn-fix-rules-")
	if err != nil {
		return
	}
	defer os.RemoveAll(dir)
	files := map[string][]byte{} // a copy's path → the output as the tool read it
	copyOf := map[*fixPosition]string{}
	for _, p := range open {
		rel, err := filepath.Rel(".", p.out)
		if err != nil || strings.HasPrefix(rel, "..") {
			rel = filepath.Base(p.out)
		}
		path := filepath.Join(dir, rel)
		files[path] = p.data
		copyOf[p] = path
	}
	var paths []string
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var fixers []string
	for f := range candidates {
		fixers = append(fixers, f)
	}
	sort.Strings(fixers)
	matches := map[*fixPosition][]string{}
	for _, fixer := range fixers {
		for path, data := range files {
			os.MkdirAll(filepath.Dir(path), 0o755)
			os.WriteFile(path, data, 0o644)
		}
		args := append(append(append([]string(nil), prefix...), config...),
			"--rules="+fixer, "--allow-risky=yes", "--using-cache=no", "--show-progress=none", "--quiet", "--")
		cmd := exec.Command(args[0], append(args[1:], paths...)...)
		if cmd.Run() != nil {
			continue
		}
		solo := map[string][]fix.Hunk{}
		for path, data := range files {
			if now, err := os.ReadFile(path); err == nil && !bytes.Equal(now, data) {
				solo[path] = fix.Diff(data, now)
			}
		}
		for _, p := range open {
			if slices.Contains(solo[copyOf[p]], p.hunk) {
				matches[p] = append(matches[p], fixer)
			}
		}
	}
	for _, p := range open {
		switch m := matches[p]; {
		case len(m) == 1:
			p.rule = m[0]
		case len(m) > 1:
			p.rule = "one of " + strings.Join(m, ", ")
		}
	}
}

// fixerPrefix is the command up to php-cs-fixer's `fix`, and the command's
// --config option, which the narrowing runs keep: the configuration's
// indentation and line endings still apply when --rules replaces its rules.
func fixerPrefix(command []string) (prefix, config []string) {
	for i, a := range command {
		if a == "fix" {
			prefix = command[:i+1]
			for j := i + 1; j < len(command) && command[j] != "--"; j++ {
				switch b := command[j]; {
				case strings.HasPrefix(b, "--config="):
					config = append(config, b)
				case b == "--config" && j+1 < len(command):
					config = append(config, b, command[j+1])
				}
			}
			break
		}
	}
	return prefix, config
}
