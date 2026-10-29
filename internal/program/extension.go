package program

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/phaenonlang/phaenon/internal/lexer"
)

// The PHP extensions the embedded stubs do not cover.
//
// php-src's stubs describe the extensions that ship with PHP. A project's
// `php` has more loaded — ext-mongodb, redis, amqp, apcu — and for those
// `php` itself is the oracle: a stub is generated from its reflection, once
// per extension version and PHP version, cached under the user's cache
// directory, and read like any other stub. Reflection knows the PHP
// signatures but no docblocks, so a generated stub has no generics; the
// overlay can refine what matters.

// Extension is one extension `php` reports as loaded.
type Extension struct {
	Name    string
	Version string
}

// LoadExtensions declares the extensions php has loaded that embedded does
// not cover, and returns the names it declared. cacheDir may be "" to
// generate without caching. A php that cannot be run is an error; one
// extension failing to generate or parse is reported through the returned
// error too, after the others were loaded.
func (p *Program) LoadExtensions(php, cacheDir string, embedded func(lower string) bool) ([]string, error) {
	exts, phpVersion, err := LoadedExtensions(php)
	if err != nil {
		return nil, err
	}
	var loaded []string
	var problems []string
	for _, e := range exts {
		lower := strings.ToLower(e.Name)
		lower = strings.TrimPrefix(lower, "zend ") // "Zend OPcache" is ext/opcache
		if embedded(lower) {
			continue
		}
		src, err := cachedExtensionStub(php, cacheDir, e, phpVersion)
		if err != nil {
			problems = append(problems, e.Name+": "+err.Error())
			continue
		}
		if len(bytes.TrimSpace(src)) == 0 {
			continue
		}
		if _, err := p.AddFile("<ext>/"+lower+".php", src, lexer.ModePHP, false); err != nil {
			problems = append(problems, e.Name+": "+err.Error())
			continue
		}
		loaded = append(loaded, e.Name)
	}
	if len(problems) > 0 {
		return loaded, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return loaded, nil
}

// LoadedExtensions asks php for its loaded extensions and its version.
func LoadedExtensions(php string) ([]Extension, string, error) {
	out, err := exec.Command(php, "-r", listExtensionsScript).Output()
	if err != nil {
		return nil, "", fmt.Errorf("running %s: %w", php, err)
	}
	var reply struct {
		PHP string     `json:"php"`
		Ext [][]string `json:"ext"`
	}
	if err := json.Unmarshal(out, &reply); err != nil {
		return nil, "", fmt.Errorf("reading the extension list from %s: %w", php, err)
	}
	exts := make([]Extension, 0, len(reply.Ext))
	for _, e := range reply.Ext {
		if len(e) == 2 {
			exts = append(exts, Extension{Name: e[0], Version: e[1]})
		}
	}
	return exts, reply.PHP, nil
}

// GenerateExtensionStub renders the declarations of one loaded extension
// as PHP, from php's reflection.
func GenerateExtensionStub(php, name string) ([]byte, error) {
	cmd := exec.Command(php, "-r", generateStubScript, "--", name)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("reflecting ext-%s: %v: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return patchStub(out), nil
}

func cachedExtensionStub(php, cacheDir string, e Extension, phpVersion string) ([]byte, error) {
	if cacheDir == "" {
		return GenerateExtensionStub(php, e.Name)
	}
	file := fmt.Sprintf("%s-%s-php%s.php", fileSafe(e.Name), fileSafe(e.Version), fileSafe(phpVersion))
	path := filepath.Join(cacheDir, "phaenon", "ext", file)
	if src, err := os.ReadFile(path); err == nil {
		return src, nil
	}
	src, err := GenerateExtensionStub(php, e.Name)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
		_ = os.WriteFile(path, src, 0o644) // a cache miss next time is the only consequence
	}
	return src, nil
}

func fileSafe(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		}
		return '_'
	}, s)
}

const listExtensionsScript = `
$ext = [];
foreach (get_loaded_extensions() as $name) {
    $ext[] = [$name, (string) (phpversion($name) ?: '')];
}
echo json_encode(['php' => PHP_VERSION, 'ext' => $ext]);
`

// generateStubScript writes one extension's classes, functions and
// constants as PHP declarations, every class name fully qualified so the
// namespace blocks cannot capture it. Defaults reflection cannot read are
// written as UNKNOWN, php-src's own convention.
const generateStubScript = `
$ext = new ReflectionExtension($argv[1]);
$out = [];

function splitName(string $fqn): array {
    $i = strrpos($fqn, '\\');
    return $i === false ? ['', $fqn] : [substr($fqn, 0, $i), substr($fqn, $i + 1)];
}
function add(string $ns, string $text): void {
    global $out;
    $out[$ns][] = $text;
}
function typeOf(?ReflectionType $t): string {
    if ($t === null) {
        return '';
    }
    if ($t instanceof ReflectionNamedType) {
        $n = $t->getName();
        $plain = $t->isBuiltin() || in_array(strtolower($n), ['self', 'static', 'parent'], true);
        $s = $plain ? $n : '\\' . $n;
        if ($t->allowsNull() && !in_array(strtolower($n), ['mixed', 'null'], true)) {
            $s = '?' . $s;
        }
        return $s;
    }
    if ($t instanceof ReflectionUnionType) {
        return implode('|', array_map('typeOf', $t->getTypes()));
    }
    if ($t instanceof ReflectionIntersectionType) {
        return implode('&', array_map('typeOf', $t->getTypes()));
    }
    return '';
}
function value(mixed $v): string {
    if ($v instanceof UnitEnum) {
        return '\\' . $v::class . '::' . $v->name;
    }
    if (is_object($v) || is_resource($v)) {
        return 'UNKNOWN';
    }
    return var_export($v, true);
}
function constantName(string $n): string {
    if (str_contains($n, '::')) {
        [$c, $k] = explode('::', $n, 2);
        if (!in_array(strtolower($c), ['self', 'static', 'parent'], true)) {
            $c = '\\' . ltrim($c, '\\');
        }
        return $c . '::' . $k;
    }
    return '\\' . ltrim($n, '\\');
}
function params(array $ps): string {
    $parts = [];
    foreach ($ps as $p) {
        $s = '';
        $t = typeOf($p->getType());
        if ($t !== '') {
            $s .= $t . ' ';
        }
        if ($p->isPassedByReference()) {
            $s .= '&';
        }
        if ($p->isVariadic()) {
            $s .= '...';
        }
        $s .= '$' . $p->getName();
        if (!$p->isVariadic() && $p->isOptional()) {
            if ($p->isDefaultValueAvailable()) {
                $s .= ' = ' . ($p->isDefaultValueConstant() ? constantName($p->getDefaultValueConstantName()) : value($p->getDefaultValue()));
            } else {
                $s .= ' = UNKNOWN';
            }
        }
        $parts[] = $s;
    }
    return implode(', ', $parts);
}
function vis(ReflectionMethod|ReflectionProperty|ReflectionClassConstant $m): string {
    return $m->isPrivate() ? 'private' : ($m->isProtected() ? 'protected' : 'public');
}
function renderFn(ReflectionFunctionAbstract $f, string $name, string $indent, string $modifiers, bool $body): string {
    $ret = $f->getReturnType();
    if ($ret === null && $f instanceof ReflectionMethod && $f->hasTentativeReturnType()) {
        $ret = $f->getTentativeReturnType();
    }
    $s = $indent . $modifiers . 'function ' . ($f->returnsReference() ? '&' : '') . $name . '(' . params($f->getParameters()) . ')';
    if ($ret !== null) {
        $s .= ': ' . typeOf($ret);
    }
    $s .= $body ? ' {}' : ';';
    if ($f->isDeprecated()) {
        $s = $indent . "/** @deprecated */\n" . $s;
    }
    return $s;
}

foreach ($ext->getClassNames() as $name) {
    $r = new ReflectionClass($name);
    [$ns, $short] = splitName($r->getName());
    $ifaces = $r->getInterfaceNames();
    if ($r->isEnum()) {
        $e = new ReflectionEnum($name);
        $head = 'enum ' . $short . ($e->isBacked() ? ': ' . typeOf($e->getBackingType()) : '');
        $ifaces = array_values(array_filter($ifaces, fn ($i) => !in_array($i, ['UnitEnum', 'BackedEnum'], true)));
    } elseif ($r->isInterface()) {
        $head = 'interface ' . $short;
    } elseif ($r->isTrait()) {
        $head = 'trait ' . $short;
    } else {
        $head = ($r->isFinal() ? 'final ' : '') . ($r->isAbstract() ? 'abstract ' : '') . ($r->isReadOnly() ? 'readonly ' : '') . 'class ' . $short;
    }
    $qualify = fn (string $i) => '\\' . $i;
    if ($r->isInterface()) {
        if ($ifaces) {
            $head .= ' extends ' . implode(', ', array_map($qualify, $ifaces));
        }
    } else {
        if ($parent = $r->getParentClass()) {
            $head .= ' extends \\' . $parent->getName();
        }
        if ($ifaces) {
            $head .= ' implements ' . implode(', ', array_map($qualify, $ifaces));
        }
    }
    $lines = [];
    foreach ($r->getReflectionConstants() as $c) {
        if ($c->getDeclaringClass()->getName() !== $r->getName()) {
            continue;
        }
        if ($c->isEnumCase()) {
            $v = $c->getValue();
            $lines[] = '    case ' . $c->getName() . ($v instanceof BackedEnum ? ' = ' . value($v->value) : '') . ';';
            continue;
        }
        $lines[] = '    ' . ($c->isFinal() ? 'final ' : '') . vis($c) . ' const ' . $c->getName() . ' = ' . value($c->getValue()) . ';';
    }
    if (!$r->isInterface()) {
        foreach ($r->getProperties() as $p) {
            if ($p->getDeclaringClass()->getName() !== $r->getName()) {
                continue;
            }
            $s = '    ' . vis($p) . ($p->isStatic() ? ' static' : '') . ($p->isReadOnly() ? ' readonly' : '');
            $t = typeOf($p->getType());
            if ($t !== '') {
                $s .= ' ' . $t;
            }
            $s .= ' $' . $p->getName();
            if ($p->hasDefaultValue()) {
                $d = $p->getDefaultValue();
                if ($d !== null || ($t !== '' && !$p->isReadOnly())) {
                    $s .= ' = ' . value($d);
                }
            }
            $lines[] = $s . ';';
        }
    }
    foreach ($r->getMethods() as $m) {
        if ($m->getDeclaringClass()->getName() !== $r->getName()) {
            continue;
        }
        $mods = '';
        if (!$r->isInterface()) {
            $mods .= ($m->isFinal() ? 'final ' : '') . ($m->isAbstract() ? 'abstract ' : '');
        }
        $mods .= vis($m) . ($m->isStatic() ? ' static ' : ' ');
        $lines[] = renderFn($m, $m->getName(), '    ', $mods, !$m->isAbstract());
    }
    add($ns, $head . "\n{\n" . implode("\n", $lines) . "\n}");
}
foreach ($ext->getFunctions() as $f) {
    [$ns, $short] = splitName($f->getName());
    add($ns, renderFn($f, $short, '', '', true));
}
foreach ($ext->getConstants() as $n => $v) {
    [$ns, $short] = splitName($n);
    add($ns, 'const ' . $short . ' = ' . value($v) . ';');
}
if ($out === []) {
    exit(0);
}
echo "<?php\n// Generated by phn from the reflection of ext-" . $ext->getName() . ' ' . $ext->getVersion() . ' (PHP ' . PHP_VERSION . ").\n\n";
ksort($out);
foreach ($out as $ns => $decls) {
    echo 'namespace' . ($ns === '' ? '' : ' ' . $ns) . " {\n\n" . implode("\n\n", $decls) . "\n\n}\n\n";
}
`
