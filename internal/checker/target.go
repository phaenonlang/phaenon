package checker

import (
	"fmt"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/phpversion"
)

// target is the PHP version the program compiles for
// (design/php-versions.md §2).
func (c *Checker) target() phpversion.Version { return c.p.Target() }

// needsPHP reports a construct PHP v added that the target lacks and the
// emitter does not lower, with one P1002: "<what> needs PHP 8.6 (target
// 8.5)" plus an optional hint. It reports nothing and returns false when
// the target has v.
func (c *Checker) needsPHP(span ast.Span, v phpversion.Version, what, hint string) bool {
	t := c.target()
	if t.AtLeast(v) {
		return false
	}
	msg := fmt.Sprintf("%s needs PHP %s (target %s)", what, v, t)
	if hint != "" {
		msg += ": " + hint
	}
	c.report(span, P1002, "%s", msg)
	return true
}
