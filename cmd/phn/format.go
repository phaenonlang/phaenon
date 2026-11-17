package main

import (
	"bufio"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/trace"
)

// The formats phn check writes its findings in (design/tooling.md §7.1).
var checkFormats = []string{"text", "json", "github", "checkstyle"}

// writeJSON writes one JSON object per finding, a line each. A line is
// JSON whatever a path or a message holds: a control character is written
// as its \u escape and bytes that are no UTF-8 as U+FFFD, where Go's own
// quoting (%q) writes \x00 and \U0001F600, which no JSON reader takes.
// `<`, `>` and `&` stay as they are: the types in a message are read by
// people too.
func writeJSON(w io.Writer, diags []checker.Diagnostic) {
	for _, d := range diags {
		fmt.Fprintf(w, `{"file":%s,"line":%d,"col":%d,"endLine":%d,"endCol":%d,"severity":%s,"code":%s,"message":%s}`+"\n",
			jsonString(d.File), d.Line, d.Col, d.EndLine, d.EndCol, jsonString(d.Severity.String()), jsonString(d.Code), jsonString(d.Message))
	}
}

// jsonString is s as a JSON string.
func jsonString(s string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return `""` // a string always encodes
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// writeGitHub writes each finding as the workflow command that makes it an
// annotation of the job: the file by its path in the repository
// (trace.AnnotationPath), which GitHub matches, the code as the title. The
// end column is written for a finding on one line only, where the runner
// reads it, and is inclusive there.
func writeGitHub(w io.Writer, diags []checker.Diagnostic) {
	for _, d := range diags {
		props := []trace.Prop{{Key: "file", Value: trace.AnnotationPath(d.File)}, {Key: "line", Value: strconv.Itoa(d.Line)}}
		if d.EndLine >= d.Line {
			props = append(props, trace.Prop{Key: "endLine", Value: strconv.Itoa(d.EndLine)})
		}
		props = append(props, trace.Prop{Key: "col", Value: strconv.Itoa(d.Col)})
		if d.EndLine == d.Line && d.EndCol > d.Col {
			props = append(props, trace.Prop{Key: "endColumn", Value: strconv.Itoa(d.EndCol - 1)})
		}
		props = append(props, trace.Prop{Key: "title", Value: d.Code})
		fmt.Fprintln(w, trace.Command{Name: d.Severity.String(), Props: props, Message: d.Message})
	}
}

// writeCheckstyle writes the findings as a Checkstyle report, a <file> per
// file in the order the findings come, the code as each error's source.
func writeCheckstyle(w io.Writer, diags []checker.Diagnostic) {
	b := bufio.NewWriter(w)
	defer b.Flush()
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n<checkstyle>\n")
	var order []string
	byFile := map[string][]checker.Diagnostic{}
	for _, d := range diags {
		if _, seen := byFile[d.File]; !seen {
			order = append(order, d.File)
		}
		byFile[d.File] = append(byFile[d.File], d)
	}
	for _, file := range order {
		fmt.Fprintf(b, "  <file name=\"%s\">\n", xmlAttr(reportPath(file)))
		for _, d := range byFile[file] {
			fmt.Fprintf(b, "    <error line=\"%d\" column=\"%d\" severity=\"%s\" message=\"%s\" source=\"%s\"/>\n",
				d.Line, d.Col, d.Severity, xmlAttr(d.Message), xmlAttr(d.Code))
		}
		b.WriteString("  </file>\n")
	}
	b.WriteString("</checkstyle>\n")
}

func xmlAttr(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// reportPath is a finding's path as a Checkstyle report names it: relative
// to the working directory when it lies under it, with forward slashes.
func reportPath(p string) string {
	if filepath.IsAbs(p) {
		if wd, err := os.Getwd(); err == nil {
			if rel, err := filepath.Rel(wd, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				p = rel
			}
		}
	}
	return filepath.ToSlash(p)
}
