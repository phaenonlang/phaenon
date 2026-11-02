// Package phpversion knows the PHP versions Phaenon compiles for
// (design/php-versions.md §2): a project's target is the oldest PHP the
// emitted code must run on, a major.minor version between Floor and Newest.
//
// The target comes from phaenon.json's "php", and otherwise from the lowest
// version composer.json's require.php admits; LowestAdmitted reads that
// floor out of a Composer constraint the way composer/semver parses it.
package phpversion

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Version is a PHP release line, major.minor: patch releases never change
// what a target means.
type Version struct{ Major, Minor int }

var (
	// PHP85 and PHP86 are the release lines phn knows.
	PHP85 = Version{8, 5}
	PHP86 = Version{8, 6}
	// Floor is the oldest target (§2.2).
	Floor = PHP85
	// Newest is the newest release phn supports: its grammar is parsed and
	// its stubs are embedded.
	Newest = PHP86
)

func (v Version) String() string { return fmt.Sprintf("%d.%d", v.Major, v.Minor) }

// Less reports whether v is older than o.
func (v Version) Less(o Version) bool {
	return v.Major < o.Major || v.Major == o.Major && v.Minor < o.Minor
}

// AtLeast reports whether v is o or newer: target.AtLeast(PHP86) asks
// whether code may use what PHP 8.6 added.
func (v Version) AtLeast(o Version) bool { return !v.Less(o) }

// IsZero reports whether v is the zero Version, which no target is.
func (v Version) IsZero() bool { return v == Version{} }

// Supported reports whether phn can compile for v.
func (v Version) Supported() bool { return v.AtLeast(Floor) && !Newest.Less(v) }

// Clamp returns the supported version nearest to v.
func (v Version) Clamp() Version {
	switch {
	case v.Less(Floor):
		return Floor
	case Newest.Less(v):
		return Newest
	}
	return v
}

var versionRe = regexp.MustCompile(`^(\d+)\.(\d+)$`)

// Parse reads a target as phaenon.json writes it: "8.6".
func Parse(s string) (Version, error) {
	m := versionRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Version{}, fmt.Errorf("%q is not a PHP version: write major.minor, as in \"8.6\"", s)
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	return Version{major, minor}, nil
}

// ErrNoBound is returned by LowestAdmitted for a constraint with no lower
// bound (`*`, `<9`, `!=8.4`): it admits every PHP, so no floor exists.
var ErrNoBound = errors.New("the constraint admits every PHP version")

// LowestAdmitted returns the major.minor of the lowest version a Composer
// constraint admits: ">=8.5", "^8.5", "~8.5.0", "8.5.*" and "^8.5 || ^9.0"
// all give 8.5. It follows composer/semver's grammar: alternatives joined by
// `||` (or a single `|`), constraints within one joined by `,` or spaces,
// hyphen ranges, wildcards, the `^` and `~` operators, a `v` prefix,
// pre-release suffixes and `@stability` flags (neither moves the floor of a
// release line). An alternative that admits nothing, such as ">=8.6 <8.5",
// is ignored.
func LowestAdmitted(constraint string) (Version, error) {
	constraint = strings.TrimSpace(constraint)
	if constraint == "" {
		return Version{}, errors.New("empty constraint")
	}
	var lowest *point
	for _, alt := range orSplit.Split(constraint, -1) {
		iv, err := parseAnd(strings.TrimSpace(alt))
		if err != nil {
			return Version{}, fmt.Errorf("%q: %w", constraint, err)
		}
		if iv.empty() {
			continue
		}
		if !iv.lo.set {
			return Version{}, ErrNoBound
		}
		if lowest == nil || iv.lo.v.less(lowest.v) {
			lo := iv.lo
			lowest = &lo
		}
	}
	if lowest == nil {
		return Version{}, fmt.Errorf("%q admits no version", constraint)
	}
	return Version{lowest.v[0], lowest.v[1]}, nil
}

// triple is a version as composer compares it, down to the patch level (a
// fourth number never matters to a release line).
type triple [3]int

func (a triple) less(b triple) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// point is one end of an interval; an unset end is unbounded.
type point struct {
	v    triple
	set  bool
	incl bool
}

type interval struct{ lo, hi point }

func (iv interval) empty() bool {
	if !iv.lo.set || !iv.hi.set {
		return false
	}
	if iv.hi.v.less(iv.lo.v) {
		return true
	}
	return iv.lo.v == iv.hi.v && !(iv.lo.incl && iv.hi.incl)
}

// intersect narrows iv to what both admit.
func (iv interval) intersect(o interval) interval {
	if o.lo.set && (!iv.lo.set || iv.lo.v.less(o.lo.v) || iv.lo.v == o.lo.v && !o.lo.incl) {
		iv.lo = o.lo
	}
	if o.hi.set && (!iv.hi.set || o.hi.v.less(iv.hi.v) || iv.hi.v == o.hi.v && !o.hi.incl) {
		iv.hi = o.hi
	}
	return iv
}

var (
	orSplit     = regexp.MustCompile(`\s*\|\|?\s*`)
	hyphenRange = regexp.MustCompile(`^(\S+)\s+-\s+(\S+)$`)
	// operatorGap finds the space in `>= 8.5`, which is not a separator
	// between two constraints.
	operatorGap = regexp.MustCompile(`(>=|<=|<>|!=|==|[<>=^~])\s+`)
	stability   = regexp.MustCompile(`(?i)@(stable|rc|beta|alpha|dev)$`)
	atom        = regexp.MustCompile(`^(>=|<=|<>|!=|==|[<>=^~])?v?(\d+|[*xX])(?:\.(\d+|[*xX]))?(?:\.(\d+|[*xX]))?(?:\.(\d+|[*xX]))?(?:[-.+]?(?i:dev|alpha|a|beta|b|rc|patch|pl|p)[.-]?\d*)?(?:\+\S*)?$`)
)

// parseAnd reads one alternative: constraints that must all hold.
func parseAnd(s string) (interval, error) {
	s = stability.ReplaceAllString(s, "")
	if m := hyphenRange.FindStringSubmatch(s); m != nil {
		lo, err := parseAtom(">=" + m[1])
		if err != nil {
			return interval{}, err
		}
		hi, err := hyphenUpper(m[2])
		if err != nil {
			return interval{}, err
		}
		return lo.intersect(hi), nil
	}
	s = operatorGap.ReplaceAllString(s, "$1")
	iv := interval{}
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		a, err := parseAtom(stability.ReplaceAllString(part, ""))
		if err != nil {
			return interval{}, err
		}
		iv = iv.intersect(a)
	}
	return iv, nil
}

// hyphenUpper is the upper end of `A - B`: B itself when complete, and
// below B's next release when partial (`- 8.6` admits every 8.6.x).
func hyphenUpper(s string) (interval, error) {
	m := atom.FindStringSubmatch(s)
	if m == nil || m[1] != "" {
		return interval{}, fmt.Errorf("%q is not a version", s)
	}
	v, n, _ := components(m)
	if n >= 3 {
		return interval{hi: point{v: v, set: true, incl: true}}, nil
	}
	return interval{hi: point{v: bump(v, n-1), set: true}}, nil
}

// components reads the numbers of a matched version; n counts the ones
// written before a wildcard, which is reported.
func components(m []string) (v triple, n int, wildcard bool) {
	for i := 0; i < 3; i++ {
		c := m[2+i]
		if c == "" {
			break
		}
		if c == "*" || c == "x" || c == "X" {
			return v, n, true
		}
		v[i], _ = strconv.Atoi(c)
		n++
	}
	return v, n, false
}

// bump returns the version after v at component i, lower components zeroed:
// bump(8.5.3, 1) = 8.6.0.
func bump(v triple, i int) triple {
	if i < 0 {
		i = 0
	}
	v[i]++
	for j := i + 1; j < 3; j++ {
		v[j] = 0
	}
	return v
}

func parseAtom(s string) (interval, error) {
	m := atom.FindStringSubmatch(s)
	if m == nil {
		return interval{}, fmt.Errorf("%q is not a version constraint", s)
	}
	op := m[1]
	v, n, wildcard := components(m)
	at := func(incl bool) point { return point{v: v, set: true, incl: incl} }
	if wildcard {
		if n == 0 {
			return interval{}, nil // `*`
		}
		if op == "" || op == "=" || op == "==" {
			return interval{lo: at(true), hi: point{v: bump(v, n-1), set: true}}, nil
		}
		// `>=8.*` and the like read as the release line's first version.
	}
	switch op {
	case "", "=", "==":
		return interval{lo: at(true), hi: at(true)}, nil
	case ">=":
		return interval{lo: at(true)}, nil
	case ">":
		return interval{lo: at(false)}, nil
	case "<=":
		return interval{hi: at(true)}, nil
	case "<":
		return interval{hi: at(false)}, nil
	case "!=", "<>":
		return interval{}, nil
	case "^":
		// ^8.5.1 admits up to the next major; ^0.3 up to 0.4; ^0.0.3 up to 0.0.4.
		i := 0
		for i < n-1 && v[i] == 0 {
			i++
		}
		return interval{lo: at(true), hi: point{v: bump(v, i), set: true}}, nil
	case "~":
		// ~8 and ~8.5 admit up to 9.0; ~8.5.1 up to 8.6.
		i := n - 2
		if n == 1 {
			i = 0
		}
		return interval{lo: at(true), hi: point{v: bump(v, i), set: true}}, nil
	}
	return interval{}, fmt.Errorf("%q is not a version constraint", s)
}
