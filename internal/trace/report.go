package trace

import (
	"bytes"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/phaenonlang/phaenon/internal/linemap"
)

// Report rewrites an XML report on compiled files into one on their
// sources, and reports which format it read: "checkstyle" (PHPStan's
// --error-format=checkstyle, php-cs-fixer's), "junit" (PHPUnit's
// --log-junit, PHPStan's --error-format=junit), "clover" or "cobertura"
// (PHPUnit's --coverage-clover and --coverage-cobertura). The format is
// told by the root element. ok is false for anything else, malformed XML
// included, which the caller treats as text.
//
// What points into a compiled file that has a line map is rewritten, and
// the rest of the document is written back byte for byte. A column is
// dropped where its line is mapped, since the map holds lines. In coverage,
// every compiled line counts as the source line its forward lookup gives,
// the lines the compiler wrote included (an expression body's `return`, an
// empty body's closing brace: the code coverage sees there is the source
// line's), and the compiled lines that come from one source line merge into
// one, covered when any of them is, its count the largest of theirs (a
// count is the number of tests that ran the line, so the largest is the one
// figure the merge knows to be true). The totals the report keeps beside
// the lines follow the merge.
func (t *Tracer) Report(data []byte) (out []byte, format string, ok bool) {
	nodes, err := parseXML(data)
	if err != nil {
		return nil, "", false
	}
	root := rootElement(nodes)
	if root == nil {
		return nil, "", false
	}
	switch {
	case root.name == "checkstyle":
		format = "checkstyle"
		t.checkstyle(root)
	case root.name == "testsuites" || root.name == "testsuite":
		format = "junit"
		t.junit(root)
	case root.name == "coverage" && len(root.elements("project")) > 0:
		format = "clover"
		t.clover(root)
	case root.name == "coverage":
		format = "cobertura"
		t.cobertura(root)
	default:
		return nil, "", false
	}
	var b bytes.Buffer
	writeXML(&b, nodes)
	return b.Bytes(), format, true
}

// mapped is the local path and the map of a compiled file a report names,
// a nil map when it has none.
func (t *Tracer) mapped(path string) (string, *linemap.Map) {
	local := t.local(path)
	return local, t.mapOf(local)
}

// rewriteText rewrites the positions in every attribute and in the text
// under n: messages, failure details, PHPStan's `file.php:12` test names.
func (t *Tracer) rewriteText(n *node) {
	walk(n, nil, func(e *node, _ []*node) {
		for _, a := range e.attrs {
			if r := t.rewritePositions(a.Value); r != a.Value {
				e.setAttr(qname(a.Name), r)
			}
		}
		for _, c := range e.children {
			if c.isText {
				if r := t.rewritePositions(c.text); r != c.text {
					c.text, c.dirty = r, true
				}
			}
		}
	})
}

// checkstyle: <file name> becomes the source, its <error line> the source
// line.
func (t *Tracer) checkstyle(root *node) {
	for _, f := range root.elements("file") {
		name, _ := f.attr("name")
		local, m := t.mapped(name)
		if m == nil {
			continue
		}
		f.setAttr("name", source(local, m))
		for _, e := range f.elements("error") {
			if line, ok := e.attr("line"); ok {
				e.setAttr("line", forward(m, line))
				e.removeAttr("column")
			}
		}
	}
	t.rewriteText(root)
}

// junit: an element's file (a test suite's, a test case's) becomes the
// source, its line the source line; the failures' text holds positions too.
func (t *Tracer) junit(root *node) {
	walk(root, nil, func(e *node, _ []*node) {
		file, ok := e.attr("file")
		if !ok {
			return
		}
		local, m := t.mapped(file)
		if m == nil {
			return
		}
		e.setAttr("file", source(local, m))
		if line, ok := e.attr("line"); ok {
			e.setAttr("line", forward(m, line))
		}
	})
	t.rewriteText(root)
}

// covered is one coverage line element and the count it carries.
type covered struct {
	n     *node
	src   int
	count int
}

// mergeLines maps the line elements among n's children to source lines
// (the attribute numAttr), merges those that land on one source line when
// merge says the kind of line merges, and puts them back in source order,
// each under its count attribute. It reports how many lines and covered
// lines the merge removed.
func mergeLines(n *node, m *linemap.Map, numAttr, countAttr string, merge func(*node) bool) (lines, coveredLines int) {
	var slots []int // the children's indexes of the line elements
	var all []covered
	for i, c := range n.children {
		if c.name != "line" {
			continue
		}
		slots = append(slots, i)
		num, _ := c.attr(numAttr)
		src, err := strconv.Atoi(forward(m, num))
		if err != nil {
			src = 0
		}
		hits, _ := c.attr(countAttr)
		count, _ := strconv.Atoi(hits)
		all = append(all, covered{n: c, src: src, count: count})
	}
	if len(all) == 0 {
		return 0, 0
	}
	var kept []covered
	at := map[int]int{} // source line → index in kept, for the lines that merge
	for _, l := range all {
		if merge(l.n) {
			if i, seen := at[l.src]; seen {
				kept[i].count = max(kept[i].count, l.count)
				continue
			}
			at[l.src] = len(kept)
		}
		kept = append(kept, l)
	}
	coveredOf := func(ls []covered) int {
		k := 0
		for _, l := range ls {
			if merge(l.n) && l.count > 0 {
				k++
			}
		}
		return k
	}
	lines, coveredLines = len(all)-len(kept), coveredOf(all)-coveredOf(kept)
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].src < kept[j].src })
	for i, l := range kept {
		if l.src > 0 {
			l.n.setAttr(numAttr, strconv.Itoa(l.src))
		}
		if v, _ := l.n.attr(countAttr); v != strconv.Itoa(l.count) {
			l.n.setAttr(countAttr, strconv.Itoa(l.count))
		}
		n.children[slots[i]] = l.n
	}
	// The slots left over go, each with the indentation before it.
	drop := map[int]bool{}
	for _, s := range slots[len(kept):] {
		drop[s] = true
		if s > 0 && n.children[s-1].isText && strings.TrimSpace(n.children[s-1].text) == "" {
			drop[s-1] = true
		}
	}
	if len(drop) > 0 {
		var children []*node
		for i, c := range n.children {
			if !drop[i] {
				children = append(children, c)
			}
		}
		n.children = children
	}
	return lines, coveredLines
}

// clover: <file name> becomes the source and its <line num> the source
// line; the statement lines of one source line merge, and the metrics of
// the file, of its class when it declares one, and of the package and the
// project around it lose the statements the merge removed.
func (t *Tracer) clover(root *node) {
	walk(root, nil, func(f *node, ancestors []*node) {
		if f.name != "file" {
			return
		}
		name, _ := f.attr("name")
		local, m := t.mapped(name)
		if m == nil {
			return
		}
		f.setAttr("name", source(local, m))
		lines, coveredLines := mergeLines(f, m, "num", "count", func(l *node) bool {
			typ, _ := l.attr("type")
			return typ == "stmt"
		})
		if lines == 0 {
			return
		}
		var metrics []*node
		metrics = append(metrics, f.elements("metrics")...)
		if classes := f.elements("class"); len(classes) == 1 {
			metrics = append(metrics, classes[0].elements("metrics")...)
		}
		for _, a := range ancestors {
			metrics = append(metrics, a.elements("metrics")...)
		}
		for _, mt := range metrics {
			subtract(mt, "statements", lines)
			subtract(mt, "elements", lines)
			subtract(mt, "coveredstatements", coveredLines)
			subtract(mt, "coveredelements", coveredLines)
		}
	})
}

func subtract(n *node, attr string, by int) {
	v, ok := n.attr(attr)
	if !ok || by == 0 {
		return
	}
	if i, err := strconv.Atoi(v); err == nil {
		n.setAttr(attr, strconv.Itoa(i-by))
	}
}

// cobertura: a <class filename> becomes the source, resolved against the
// report's <source> directories, and its <line number>s, in the class's
// <lines> and in each method's, the source lines, merged per source line.
// With one <source> directory, it becomes the directory that holds both the
// compiled files and the sources, and every class's filename is rewritten
// relative to it; with several, a source's filename is relative to the
// directory its compiled file was found under. Line rates and the root's
// line totals are recomputed where a merge changed them, and a package or
// class named after its file, as PHPUnit names them, is renamed with it.
func (t *Tracer) cobertura(root *node) {
	var dirs []string
	var sourceNodes []*node
	for _, s := range root.elements("sources") {
		for _, src := range s.elements("source") {
			dirs = append(dirs, strings.TrimSpace(src.textOf()))
			sourceNodes = append(sourceNodes, src)
		}
	}
	if len(dirs) == 0 {
		dirs = []string{""} // filenames absolute, or relative to the working directory
	}
	type class struct {
		n        *node
		pkg      *node
		dir      string // the <source> it resolved against
		old      string // its filename as read
		abs      string // the file it names, the source when mapped
		mapped   bool
		modified bool // a merge changed its lines
	}
	var classes []*class
	changed := false
	for _, pkgs := range root.elements("packages") {
		for _, pkg := range pkgs.elements("package") {
			for _, cs := range pkg.elements("classes") {
				for _, c := range cs.elements("class") {
					filename, _ := c.attr("filename")
					cl := &class{n: c, pkg: pkg, old: filename}
					classes = append(classes, cl)
					for i, dir := range dirs {
						path := filename
						if !filepath.IsAbs(path) {
							path = filepath.Join(dir, filename)
						}
						local, m := t.mapped(path)
						if i == 0 {
							cl.dir, cl.abs = dir, path
						}
						if m == nil {
							continue
						}
						cl.dir, cl.abs, cl.mapped = dir, source(local, m), true
						if t.mergeCobertura(c, m) {
							cl.modified, changed = true, true
						}
						break
					}
				}
			}
		}
	}
	anyMapped := false
	for _, c := range classes {
		anyMapped = anyMapped || c.mapped
	}
	if !anyMapped {
		return
	}
	// Where the filenames are relative to.
	base := func(c *class) string { return c.dir }
	if len(sourceNodes) == 1 {
		common := dirs[0]
		for _, c := range classes {
			if c.mapped {
				common = commonDir(common, filepath.Dir(c.abs))
			}
		}
		if common != dirs[0] {
			sourceNodes[0].setText(common)
		}
		base = func(*class) string { return common }
	}
	for _, c := range classes {
		if filepath.IsAbs(c.old) && !c.mapped {
			continue
		}
		name := c.abs
		if !filepath.IsAbs(c.old) {
			if rel, err := filepath.Rel(base(c), c.abs); err == nil {
				name = rel
			}
		}
		name = filepath.ToSlash(name)
		c.n.setAttr("filename", name)
		// PHPUnit names a package after its file, and the class of a file's
		// functions too.
		if pkgName, _ := c.pkg.attr("name"); pkgName == c.old {
			c.pkg.setAttr("name", name)
		}
		if className, _ := c.n.attr("name"); className == c.old {
			c.n.setAttr("name", name)
		}
	}
	if !changed {
		return
	}
	// The rates of what changed: each package's from its classes' lines, the
	// root's from every class's.
	var valid, hit int
	byPkg := map[*node][2]int{}
	modified := map[*node]bool{}
	for _, c := range classes {
		v, h := lineTotals(c.n)
		valid += v
		hit += h
		byPkg[c.pkg] = [2]int{byPkg[c.pkg][0] + v, byPkg[c.pkg][1] + h}
		if c.modified {
			modified[c.pkg] = true
		}
	}
	for pkg := range modified {
		setRate(pkg, byPkg[pkg][0], byPkg[pkg][1])
	}
	setRate(root, valid, hit)
	if _, ok := root.attr("lines-valid"); ok {
		root.setAttr("lines-valid", strconv.Itoa(valid))
	}
	if _, ok := root.attr("lines-covered"); ok {
		root.setAttr("lines-covered", strconv.Itoa(hit))
	}
}

// mergeCobertura maps and merges the lines of a class and of its methods,
// and recomputes the line rates of those a merge changed.
func (t *Tracer) mergeCobertura(class *node, m *linemap.Map) (modified bool) {
	merge := func(owner *node) {
		for _, lines := range owner.elements("lines") {
			if removed, _ := mergeLines(lines, m, "number", "hits", func(*node) bool { return true }); removed > 0 {
				modified = true
				v, h := countLines(lines)
				setRate(owner, v, h)
			}
		}
	}
	for _, ms := range class.elements("methods") {
		for _, method := range ms.elements("method") {
			merge(method)
		}
	}
	merge(class)
	return modified
}

// lineTotals counts a class's lines and the covered ones.
func lineTotals(class *node) (valid, hit int) {
	for _, lines := range class.elements("lines") {
		v, h := countLines(lines)
		valid += v
		hit += h
	}
	return valid, hit
}

func countLines(lines *node) (valid, hit int) {
	for _, l := range lines.elements("line") {
		valid++
		if h, _ := l.attr("hits"); h != "" && h != "0" {
			hit++
		}
	}
	return valid, hit
}

// setRate sets an element's line-rate, written as PHP writes a float.
func setRate(n *node, valid, hit int) {
	if _, ok := n.attr("line-rate"); !ok {
		return
	}
	rate := 0.0
	if valid > 0 {
		rate = float64(hit) / float64(valid)
	}
	n.setAttr("line-rate", phpFloat(rate))
}

// phpFloat formats f as PHP's (string) cast does at its default precision
// of 14 significant digits: 0.97142857142857, 1, 1.0E-5.
func phpFloat(f float64) string {
	s := strconv.FormatFloat(f, 'G', 14, 64)
	mantissa, exp, ok := strings.Cut(s, "E")
	if !ok {
		return s
	}
	if !strings.Contains(mantissa, ".") {
		mantissa += ".0"
	}
	return mantissa + "E" + exp[:1] + strings.TrimLeft(exp[1:], "0")
}

// commonDir is the deepest directory that holds both a and b.
func commonDir(a, b string) string {
	a, b = filepath.Clean(a), filepath.Clean(b)
	for {
		if b == a || strings.HasPrefix(b, a+string(filepath.Separator)) || a == string(filepath.Separator) {
			return a
		}
		parent := filepath.Dir(a)
		if parent == a {
			return a
		}
		a = parent
	}
}
