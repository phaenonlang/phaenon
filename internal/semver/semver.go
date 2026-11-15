// Package semver reads the versions and the version constraints of Composer
// packages as Composer's own library, composer/semver, reads them, and says
// whether a version satisfies a constraint: the question Composer asks of
// each release of a package that another package's `conflict` names
// (design/tooling.md §3.7).
//
// It is written to read a constraint as Composer does or not at all. It
// reads less: a spelling outside the forms below is an error, which the
// caller reports as a constraint it cannot judge, never a guess. That is an
// aim, held by measurement and not proved:
//
//   - testdata/constraints.json lists constraints with Composer's answers,
//     and TestComposerAgrees asks the Composer of the machine for them, in
//     CI too;
//   - on 2026-11-13 the reader was compared with Composer 2.9.5
//     (composer/semver 3.4.4) over 1,800,000 generated constraints, thirty
//     versions each. Both read 388,732 of them and gave the same answer for
//     every version; the reader read none that Composer refuses, and
//     refused 284,709 that Composer reads.
//
// A difference found is a bug of this package and becomes a vector. Those
// found so far were of three kinds, each refused now: alternatives that
// Composer joins into something they do not say, a space Composer does not
// part a constraint at, and a space inside an operator.
//
// What is read, with Composer's quirks (VersionParser::parseConstraints):
//
//   - alternatives joined by `||` or `|`, and within one, constraints joined
//     by a comma or by spaces;
//   - the comparisons `=`, `==`, `!=`, `<>`, `<`, `<=`, `>`, `>=`, a space
//     allowed after the operator. `<1.2` and `>=1.2` are bounds at
//     1.2.0.0-dev, the first version of the release line, so that `<0.5.0`
//     leaves 0.5.0-beta1 out as it leaves 0.5.0 out; `<=1.2` and `>1.2` are
//     bounds at the release itself;
//   - `~1.2`, `^1.2`, the wildcards `1.2.*` and `*`, and the hyphen range
//     `1.0 - 2.0`, each a pair of such bounds;
//   - a `v` before a version, up to four numbers, the stability suffixes
//     (`-dev`, `-alpha1`, `-beta2`, `-RC1`, `-patch1` and their short
//     spellings), build metadata after a comparison's version, and a
//     `@stability` flag, which moves the bound of a comparison of a stable
//     version (`>=1.2@beta` is `>=1.2.0.0-beta`) and nothing else;
//   - a branch, `dev-main`, which no numbered version equals.
//
// What is not: inline aliases (`as`) and a word that ends or begins with
// those letters beside another constraint, `self.version`, numeric branches
// (`1.x-dev`), versions made of dates, a wildcard or build metadata inside
// `~`, `^` and a hyphen range, a stability flag after an operator and a
// space (Composer drops it), a comma after an operator's character, any
// space but the plain one inside a constraint, and whatever Composer itself
// refuses, `~>1.2` and `< =1.2` among it. Nor is a constraint of several
// alternatives read when one of them is a range that ends before it begins
// or is bounded by a branch: Composer joins neighbouring ranges into one,
// and with such a range the result is not what the alternatives say.
//
// Versions are ordered as PHP's version_compare orders Composer's normalised
// form: dev < alpha < beta < RC < the release < patch.
//
// internal/phpversion reads the same grammar for another question, the
// lowest PHP release line a `require.php` admits, where suffixes and
// exclusions never matter.
package semver

import (
	"cmp"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Version is a version as Composer normalises it: four numbers and what
// follows them, or a branch.
type Version struct {
	parts  []part
	branch string // the name after dev-; no parts then
	raw    string
}

// part is one element of a normalised version, as version_compare splits
// it: a number, or one of the words dev, alpha, beta, RC and patch.
type part struct {
	word string
	n    int
}

// The order of version_compare's special forms; a number stands at release.
var ranks = map[string]int{"dev": 0, "alpha": 1, "beta": 2, "RC": 3, "patch": 5}

const release = 4

func (p part) rank() int {
	if p.word == "" {
		return release
	}
	return ranks[p.word]
}

// String is the version as it was written.
func (v Version) String() string { return v.raw }

// IsBranch reports whether v names a branch (dev-main), which has no place
// in the order of the numbered versions.
func (v Version) IsBranch() bool { return v.branch != "" }

// Compare orders two numbered versions: -1 when v is the older, 1 when it
// is the newer, 0 when they are one version (1.0 and v1.0.0).
func (v Version) Compare(o Version) int {
	a, b := v.parts, o.parts
	for i := 0; i < len(a) && i < len(b); i++ {
		var c int
		if a[i].word == "" && b[i].word == "" {
			c = cmp.Compare(a[i].n, b[i].n)
		} else {
			c = cmp.Compare(a[i].rank(), b[i].rank())
		}
		if c != 0 {
			return c
		}
	}
	// The longer one is the newer when it goes on with a number, and
	// otherwise stands where its next word stands against a release.
	switch {
	case len(a) > len(b):
		return tail(a[len(b)])
	case len(b) > len(a):
		return -tail(b[len(a)])
	}
	return 0
}

func tail(p part) int {
	if p.word == "" {
		return 1
	}
	return cmp.Compare(p.rank(), release)
}

// stability is the word Composer's parseStability gives: dev, alpha, beta,
// RC, or "" for a stable version, a patch included.
func (v Version) stability() string {
	found := ""
	for _, p := range v.parts {
		switch p.word {
		case "dev":
			return "dev"
		case "alpha", "beta", "RC":
			if found == "" {
				found = p.word
			}
		}
	}
	return found
}

// with returns v followed by the word: 1.2.0.0 with dev is 1.2.0.0-dev.
func (v Version) with(word string) Version {
	v.parts = append(append([]part(nil), v.parts...), part{word: word})
	return v
}

// modifier is composer/semver's $modifierRegex: the stability, its numbers
// and the dev suffix. Its three groups are added to those before it.
const modifier = `[._-]?(?:(stable|beta|b|rc|alpha|a|patch|pl|p)((?:[.-]?\d+)*))?([.-]?dev)?`

var (
	// classical is the versioning VersionParser::normalize reads first.
	classical = regexp.MustCompile(`(?i)^v?(\d{1,5})(\.\d+)?(\.\d+)?(\.\d+)?` + modifier + `$`)
	// buildMetadata is what normalize strips: +exp.sha.5114f85.
	buildMetadata = regexp.MustCompile(`^([^,\s+]+)\+\S+$`)
	branchName    = regexp.MustCompile(`^[^,\s@#]+$`)
	digits        = regexp.MustCompile(`\d+`)
)

// ParseVersion reads a version as VersionParser::normalize does: `0.4.1`,
// `v1.2`, `1.0.0-beta2`, `dev-main`.
func ParseVersion(s string) (Version, error) {
	raw := trim(s)
	if len(raw) > 4 && strings.EqualFold(raw[:4], "dev-") {
		if !branchName.MatchString(raw[4:]) {
			return Version{}, fmt.Errorf("%q is not a version", s)
		}
		return Version{branch: raw[4:], raw: raw}, nil
	}
	text := raw
	if m := buildMetadata.FindStringSubmatch(text); m != nil {
		text = m[1]
	}
	m := classical.FindStringSubmatch(text)
	if m == nil {
		return Version{}, fmt.Errorf("%q is not a version", s)
	}
	// normalize takes `stable` in small letters for no suffix at all, and
	// in any other case for a word of its own, which nothing orders.
	stable := strings.EqualFold(m[5], "stable")
	if stable && m[5] != "stable" {
		return Version{}, fmt.Errorf("%q is not a version", s)
	}
	v := Version{raw: raw}
	for i := 1; i <= 4; i++ {
		n, err := number(strings.TrimPrefix(m[i], "."))
		if err != nil {
			return Version{}, fmt.Errorf("%q is not a version", s)
		}
		v.parts = append(v.parts, part{n: n})
	}
	if word := expand(m[5]); word != "" {
		v.parts = append(v.parts, part{word: word})
		for _, d := range digits.FindAllString(m[6], -1) {
			n, err := number(d)
			if err != nil {
				return Version{}, fmt.Errorf("%q is not a version", s)
			}
			v.parts = append(v.parts, part{n: n})
		}
	}
	if m[7] != "" && !stable {
		v.parts = append(v.parts, part{word: "dev"})
	}
	return v, nil
}

// expand is normalize's expandStability; `stable` adds nothing, and what
// follows it is dropped, as normalize drops it.
func expand(word string) string {
	switch strings.ToLower(word) {
	case "a", "alpha":
		return "alpha"
	case "b", "beta":
		return "beta"
	case "rc":
		return "RC"
	case "p", "pl", "patch":
		return "patch"
	}
	return ""
}

// number reads a number of a version; "" is the 0 of a number left out.
func number(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	if len(s) > 9 {
		return 0, fmt.Errorf("%s is too long a number", s)
	}
	return strconv.Atoi(s)
}

// Constraint is a version constraint: alternatives, each a list of
// comparisons that must all hold.
type Constraint struct {
	alternatives [][]comparison
}

// comparison is one bound, or the wildcard `*` that every version and
// every branch satisfies.
type comparison struct {
	op      string // ==, !=, <, <=, >, >=
	version Version
	any     bool
}

// Matches reports whether Composer takes v to satisfy c: for a `conflict`,
// whether the constraint keeps v out.
func (c Constraint) Matches(v Version) bool {
	for _, all := range c.alternatives {
		ok := true
		for _, one := range all {
			ok = ok && one.matches(v)
		}
		if ok {
			return true
		}
	}
	return false
}

// matches is Constraint::versionCompare for a package at version v: a
// branch equals itself and differs from everything else, and has no order.
func (c comparison) matches(v Version) bool {
	if c.any {
		return true
	}
	if v.IsBranch() || c.version.IsBranch() {
		switch c.op {
		case "!=":
			return v.branch != c.version.branch
		case "==":
			return v.branch == c.version.branch
		}
		return false
	}
	order := v.Compare(c.version)
	switch c.op {
	case "==":
		return order == 0
	case "!=":
		return order != 0
	case "<":
		return order < 0
	case "<=":
		return order <= 0
	case ">":
		return order > 0
	}
	return order >= 0
}

// numbers is composer/semver's $versionRegex without the wildcards and the
// numeric branches it also admits: the forms built on it refuse those.
const numbers = `v?(\d+)(?:\.(\d+))?(?:\.(\d+))?(?:\.(\d+))?`

const gap = "\x01"

var (
	alternatives = regexp.MustCompile(`\s*\|\|?\s*`)
	// operatorGap is the space Composer allows after a comparison's
	// operator, where it does not part two constraints; gap stands for it
	// while the constraints are parted.
	operatorGap   = regexp.MustCompile(`([<>=]) +`)
	afterOperator = regexp.MustCompile(`[<>=],`)
	// A hyphen range has one space on each side of its hyphen: Composer
	// parts the constraint at a wider gap, and does not part it at a
	// space or a comma that touches a hyphen on one side only.
	wideHyphen    = regexp.MustCompile(` {2,}-|- {2,}`)
	looseHyphen   = regexp.MustCompile(`-[ ,]|[ ,]-`)
	stabilityFlag = regexp.MustCompile(`(?i)^([^,\s]*?)@(stable|rc|beta|alpha|dev)$`)
	matchAll      = regexp.MustCompile(`(?i)^v?[x*](\.[x*])*$`)
	ranged        = regexp.MustCompile(`(?i)^` + numbers + modifier + `$`)
	wildcard      = regexp.MustCompile(`^v?(\d+)(?:\.(\d+))?(?:\.(\d+))?(?:\.[xX*])+$`)
	operator      = regexp.MustCompile(`^(<>|!=|>=?|<=?|==?)?\s*(.*)$`)
	comparator    = regexp.MustCompile(`^(<>|!=|>=?|<=?|==?)$`)
	// suffixed is the test Composer makes of a comparison's version, in
	// lower case, before it adds -dev to the bound: RC is spelt in capitals
	// there, so `<1.0-RC1` gets the -dev that `<1.0-beta1` does not.
	suffixed = regexp.MustCompile(`-[._-]?(?:(?:stable|beta|b|RC|alpha|a|patch|pl|p)(?:[.-]?\d+)*)?(?:[.-]?dev)?$`)
)

// unread is the error of a constraint, or a part of one, outside the forms
// this package reads: Composer may read it, or refuse it too.
func unread(s string) error {
	return fmt.Errorf("%q is not a version constraint phn reads", strings.ReplaceAll(s, gap, " "))
}

// ParseConstraint reads a constraint as VersionParser::parseConstraints
// does, or fails on one outside the forms this package reads. The error is
// worded to follow the constraint in a message: "… cannot be judged (…)".
func ParseConstraint(s string) (Constraint, error) {
	text := trim(s)
	if text == "" {
		return Constraint{}, fmt.Errorf("it is empty")
	}
	// Composer parts constraints at the plain space and the comma alone.
	// Any other space stays in a version, which is then none, but for the
	// few it allows around `||`: none is read here.
	if strings.ContainsFunc(text, func(r rune) bool { return r < ' ' || r == 0x7f || r != ' ' && unicode.IsSpace(r) }) {
		return Constraint{}, fmt.Errorf("it holds a tab, a line break, a control character or a space that is not the plain one")
	}
	var c Constraint
	written := alternatives.Split(text, -1)
	for _, alt := range written {
		all, err := parseAll(alt)
		if err != nil {
			return Constraint{}, err
		}
		c.alternatives = append(c.alternatives, all)
	}
	// Composer joins two neighbouring alternatives `>=A <B || >=B <C` into
	// `>=A <C` (MultiConstraint::optimizeConstraints), by the text of the two
	// B. That is the two ranges together only where neither ends before it
	// begins and no bound is a branch, which no version is below. Where one
	// does, what Composer makes of the constraint is not what its
	// alternatives say, and it is not read.
	if len(c.alternatives) > 1 {
		for i, all := range c.alternatives {
			if len(all) != 2 || all[0].op != ">=" || all[1].op != "<" && all[1].op != "<=" {
				continue
			}
			from, to := all[0].version, all[1].version
			switch {
			case from.IsBranch() || to.IsBranch():
				return Constraint{}, fmt.Errorf("%q is a range bounded by a branch, beside another alternative, and Composer joins neighbouring ranges into one", written[i])
			case from.Compare(to) > 0:
				return Constraint{}, fmt.Errorf("%q is a range that ends before it begins, beside another alternative, and Composer joins neighbouring ranges into one", written[i])
			}
		}
	}
	return c, nil
}

// trim is PHP's trim, which Composer applies to a constraint and to a
// version: it takes off the plain space, the tab, the line break, the
// carriage return, the vertical tab and the NUL byte, and no other space.
func trim(s string) string {
	return strings.Trim(s, " \t\n\r\x0b\x00")
}

// parseAll reads one alternative: constraints that must all hold.
func parseAll(s string) ([]comparison, error) {
	if s == "" {
		return nil, fmt.Errorf("an alternative of it is empty")
	}
	if wideHyphen.MatchString(s) || looseHyphen.MatchString(strings.ReplaceAll(s, " - ", "\x00")) {
		return nil, fmt.Errorf("a space or a comma beside a hyphen parts nothing: a hyphen range is written 1.0 - 2.0")
	}
	// Nor does Composer part a constraint at a comma that follows an
	// operator's character: `<1.0+b=,>=0.2` is `<1.0` with build metadata.
	if afterOperator.MatchString(s) {
		return nil, fmt.Errorf("a comma after <, > or = parts nothing")
	}
	s = operatorGap.ReplaceAllString(s, "$1"+gap)
	// Composer does not part a constraint before or after the letters
	// `as`, which write an alias: `dev-atlas <2` is one branch name to it.
	words := strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' })
	for i, word := range words {
		if i > 0 && strings.HasPrefix(word, "as") || i < len(words)-1 && strings.HasSuffix(word, "as") {
			return nil, fmt.Errorf("%q stands beside another constraint, where Composer reads the letters `as` as an alias", strings.ReplaceAll(word, gap, " "))
		}
	}
	var all []comparison
	for _, group := range strings.Split(s, ",") {
		fields := strings.FieldsFunc(group, func(r rune) bool { return r == ' ' })
		if len(fields) == 0 {
			return nil, fmt.Errorf("a comma of it parts nothing")
		}
		for i := 0; i < len(fields); i++ {
			var (
				bounds []comparison
				err    error
			)
			switch {
			case fields[i] == "-":
				return nil, fmt.Errorf("a hyphen range needs a version on each side")
			case i+2 < len(fields) && fields[i+1] == "-":
				bounds, err = parseHyphen(fields[i], fields[i+2])
				i += 2
			case i+1 < len(fields) && fields[i+1] == "-":
				return nil, fmt.Errorf("a hyphen range needs a version on each side")
			default:
				bounds, err = parseOne(fields[i])
			}
			if err != nil {
				return nil, err
			}
			all = append(all, bounds...)
		}
	}
	return all, nil
}

// parseOne reads one constraint, in the order VersionParser::parseConstraint
// tries its forms.
func parseOne(s string) ([]comparison, error) {
	if before, after, spaced := strings.Cut(s, gap); spaced {
		written := strings.ReplaceAll(s, gap, " ")
		switch {
		case !comparator.MatchString(before) || strings.Contains(after, gap):
			// The space stays in the version, which is then none.
			return nil, unread(s)
		case strings.Contains(after, "@"):
			// Composer looks for the flag before it takes the operator
			// off, in a constraint without a space, and reads `< 1.0@beta`
			// as `<1.0`.
			return nil, fmt.Errorf("%q has a stability flag after an operator and a space, where Composer drops the flag", written)
		}
		// What follows the space is the version: in `< =1.0` it is `=1.0`,
		// which is none, and the two are not read together as `<=1.0`.
		return compare(before, after, "", s)
	}
	flag := ""
	if m := stabilityFlag.FindStringSubmatch(s); m != nil {
		// Composer appends the flag to the bound as it is written, and only
		// these spellings are words version_compare orders.
		switch m[2] {
		case "stable":
		case "RC", "rc":
			flag = "RC"
		case "beta", "alpha", "dev":
			flag = m[2]
		default:
			return nil, fmt.Errorf("the stability flag of %q is spelt @stable, @RC, @beta, @alpha or @dev", s)
		}
		if s = m[1]; s == "" {
			s = "*"
		}
	}
	if strings.Contains(s, "@") {
		return nil, unread(s)
	}
	// A reference after a branch (dev-main#abc123) names a commit, not
	// another version; a # with nothing after it stays in the name, which
	// is then not read.
	if len(s) > 4 && strings.EqualFold(s[:4], "dev-") {
		if name, ref, _ := strings.Cut(s, "#"); ref != "" {
			s = name
		}
	}
	switch {
	case s == "*" || s == "x" || s == "X":
		return []comparison{{any: true}}, nil
	case matchAll.MatchString(s):
		// v* and *.* are every numbered version, and no branch.
		return []comparison{{op: ">=", version: bound([4]int{})}}, nil
	case strings.HasPrefix(s, "~>"):
		return nil, fmt.Errorf("%q uses the operator ~>, which Composer does not have: ~ is its spelling", s)
	case strings.HasPrefix(s, "~"), strings.HasPrefix(s, "^"):
		return parseRange(s)
	}
	if m := wildcard.FindStringSubmatch(s); m != nil {
		return parseWildcard(s, m)
	}
	m := operator.FindStringSubmatch(s)
	return compare(m[1], m[2], flag, s)
}

// compare reads a comparison: its operator, none for =, the text of its
// version and the stability flag that followed it; written is the whole
// constraint, for the error.
func compare(op, text, flag, written string) ([]comparison, error) {
	v, err := ParseVersion(text)
	if err != nil {
		return nil, unread(written)
	}
	switch op {
	case "", "=":
		op = "=="
	case "<>":
		op = "!="
	}
	switch {
	case v.IsBranch():
	case op != "==" && flag != "" && v.stability() == "":
		v = v.with(flag)
	case (op == "<" || op == ">=") && !suffixed.MatchString(strings.ToLower(text)):
		v = v.with("dev")
	}
	return []comparison{{op: op, version: v}}, nil
}

// written reads the numbers of a version a range is built on, the groups
// of m from 1 to last: how many were written, and their values, the ones
// left out as 0.
func written(m []string, last int) (count int, nums [4]int, err error) {
	for i := range last {
		if m[i+1] == "" {
			break
		}
		if nums[i], err = number(m[i+1]); err != nil {
			return 0, nums, err
		}
		count++
	}
	return count, nums, nil
}

// bound is the first version of a release line, a.b.c.d-dev: the bound of
// every range.
func bound(nums [4]int) Version {
	v := Version{}
	for _, n := range nums {
		v.parts = append(v.parts, part{n: n})
	}
	return v.with("dev")
}

// next is nums with the number at position (from 1) raised by one and the
// ones after it zeroed: manipulateVersionString.
func next(nums [4]int, position int) [4]int {
	nums[position-1]++
	for i := position; i < len(nums); i++ {
		nums[i] = 0
	}
	return nums
}

// lower is the lower bound of a range written from the version text: the
// version itself when it names a stability, else the first of its line.
func lower(text string, m []string) (Version, error) {
	v, err := ParseVersion(text)
	if err != nil {
		return Version{}, err
	}
	if m[5] == "" && m[7] == "" {
		v = v.with("dev")
	}
	return v, nil
}

// parseRange reads ~1.2 and ^1.2: from the version to the next release
// that may break it. ~ raises the number before the last one written; ^
// raises the first number that is not 0.
func parseRange(s string) ([]comparison, error) {
	m := ranged.FindStringSubmatch(s[1:])
	if m == nil {
		return nil, unread(s)
	}
	count, nums, err := written(m, 4)
	if err != nil {
		return nil, unread(s)
	}
	low, err := lower(s[1:], m)
	if err != nil {
		return nil, unread(s)
	}
	position := max(1, count-1)
	if s[0] == '^' {
		// Composer compares the text with "0": ^00.3 is ^0, not ^0.3.
		switch {
		case m[1] != "0" || count < 2:
			position = 1
		case m[2] != "0" || count < 3:
			position = 2
		default:
			position = 3
		}
	}
	return []comparison{{op: ">=", version: low}, {op: "<", version: bound(next(nums, position))}}, nil
}

// parseWildcard reads 1.2.*: the release line of the numbers written.
func parseWildcard(s string, m []string) ([]comparison, error) {
	count, nums, err := written(m, 3)
	if err != nil {
		return nil, unread(s)
	}
	below := comparison{op: "<", version: bound(next(nums, count))}
	if nums == [4]int{} {
		return []comparison{below}, nil
	}
	return []comparison{{op: ">=", version: bound(nums)}, below}, nil
}

// parseHyphen reads `from - to`: from the first of from's line to to
// itself when to is written in full or names a stability, else to the
// last of to's line (1.0 - 2.0 admits every 2.0.x).
func parseHyphen(from, to string) ([]comparison, error) {
	fm, tm := ranged.FindStringSubmatch(from), ranged.FindStringSubmatch(to)
	if fm == nil || tm == nil {
		return nil, unread(from + " - " + to)
	}
	low, err := lower(from, fm)
	if err != nil {
		return nil, unread(from + " - " + to)
	}
	high, err := ParseVersion(to)
	if err != nil {
		return nil, unread(from + " - " + to)
	}
	count, nums, err := written(tm, 4)
	if err != nil {
		return nil, unread(from + " - " + to)
	}
	above := comparison{op: "<=", version: high}
	if count < 3 && tm[5] == "" && tm[7] == "" {
		above = comparison{op: "<", version: bound(next(nums, count))}
	}
	return []comparison{{op: ">=", version: low}, above}, nil
}
