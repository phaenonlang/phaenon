package checker

// Severity of a diagnostic.
type Severity uint8

const (
	Error Severity = iota
	Warning
)

func (s Severity) String() string {
	if s == Warning {
		return "warning"
	}
	return "error"
}

// Code identifies a diagnostic family member. The families are P1 (syntax
// and removed constructs), P2 (types and flow), P3 (generics, M3) and P4
// (interop: vendor code, docblocks). Numbers are stable once published.
type Code struct {
	ID       string
	Severity Severity
	Title    string
}

var (
	// P1 — removed constructs, and constructs the target PHP lacks
	P1001 = Code{"P1001", Error, "construct removed from Phaenon"}
	// P1002: "… needs PHP 8.6 (target 8.5)" for a construct newer than the
	// target that is not lowered (design/php-versions.md §3).
	P1002 = Code{"P1002", Error, "construct needs a newer PHP than the target"}
	// P1003: what PHP 8.6's compiler rejects where placeholders stand
	// (design/php-versions.md §5.1); the grammar admits it.
	P1003 = Code{"P1003", Error, "placeholder where PHP forbids one"}

	// P11xx — constructs PHP deprecates, reported whatever the target
	// unless --no-deprecations (design/php-versions.md §4).
	P1101 = Code{"P1101", Warning, "construct deprecated by PHP"}

	// P2 — names
	P2001 = Code{"P2001", Error, "unknown class"}
	P2002 = Code{"P2002", Error, "unknown function"}
	P2003 = Code{"P2003", Error, "unknown constant"}
	P2004 = Code{"P2004", Error, "unknown class constant"}
	P2005 = Code{"P2005", Error, "unknown property"}
	P2006 = Code{"P2006", Error, "unknown method"}
	P2007 = Code{"P2007", Error, "unknown static property"}
	P2008 = Code{"P2008", Error, "undefined variable"}
	P2009 = Code{"P2009", Error, "variable might not be defined"}
	P2010 = Code{"P2010", Error, "unknown named argument"}
	P2011 = Code{"P2011", Error, "parent without a parent class"}
	P2012 = Code{"P2012", Error, "self or static outside a class"}
	P2013 = Code{"P2013", Warning, "unused import"}
	P2014 = Code{"P2014", Error, "unknown enum case"}

	// P2 — calls
	P2101 = Code{"P2101", Error, "too few arguments"}
	P2102 = Code{"P2102", Error, "too many arguments"}
	P2103 = Code{"P2103", Error, "argument type mismatch"}
	P2105 = Code{"P2105", Error, "member access on a non-object"}
	P2106 = Code{"P2106", Error, "static call of an instance method"}
	P2107 = Code{"P2107", Error, "cannot instantiate"}
	P2108 = Code{"P2108", Error, "member not accessible"}
	P2109 = Code{"P2109", Error, "value is not callable"}
	P2110 = Code{"P2110", Error, "positional argument after a named one"}
	P2111 = Code{"P2111", Error, "instance call of a static member"}
	P2112 = Code{"P2112", Error, "by-reference argument is not a variable"}
	P2113 = Code{"P2113", Error, "argument is mixed where a type is required"}
	// P2114: `$xs |> sort(...)` — PHP passes the piped value as a
	// temporary, so a by-reference parameter is an Error at run time
	// (design/php-versions.md §5.4).
	P2114 = Code{"P2114", Error, "piped value passed by reference"}

	// P2 — assignments and declared types
	P2201 = Code{"P2201", Error, "assigned value does not match the property type"}
	P2202 = Code{"P2202", Error, "returned value does not match the return type"}
	P2203 = Code{"P2203", Error, "missing return"}
	P2204 = Code{"P2204", Error, "return with a value in a void function"}
	P2205 = Code{"P2205", Error, "result of a void function used"}
	P2206 = Code{"P2206", Error, "write to a readonly property"}
	P2207 = Code{"P2207", Error, "write to a property with restricted set visibility"}
	P2208 = Code{"P2208", Error, "default value does not match the parameter type"}
	P2209 = Code{"P2209", Error, "a never function returns"}
	P2210 = Code{"P2210", Error, "static property accessed through an instance"}
	P2211 = Code{"P2211", Error, "constant value does not match its type"}
	P2212 = Code{"P2212", Error, "the @var type is not a subtype of the assigned value's type"}

	// P2 — expressions
	P2301 = Code{"P2301", Error, "invalid operand types"}
	P2302 = Code{"P2302", Error, "offset access on a non-array"}
	P2303 = Code{"P2303", Error, "foreach over a non-iterable"}
	P2304 = Code{"P2304", Error, "match arm missing"}
	P2305 = Code{"P2305", Error, "strict comparison between incompatible types"}
	P2306 = Code{"P2306", Error, "instanceof always has the same result"}
	P2307 = Code{"P2307", Error, "nullsafe operator on a non-nullable value"}
	P2308 = Code{"P2308", Error, "null coalescing on a value that is never null"}
	P2309 = Code{"P2309", Error, "invalid cast"}
	P2310 = Code{"P2310", Error, "clone of a non-object"}
	P2311 = Code{"P2311", Error, "$this outside an object context"}
	P2312 = Code{"P2312", Error, "string conversion of a non-stringable value"}
	P2313 = Code{"P2313", Error, "offset does not exist"}
	P2314 = Code{"P2314", Error, "isset on a value that is never null"}
	P2315 = Code{"P2315", Error, "yield outside a generator"}
	P2316 = Code{"P2316", Error, "unknown offset type"}
	P2317 = Code{"P2317", Error, "throwing a non-Throwable"}
	P2318 = Code{"P2318", Error, "catching a non-Throwable"}

	// P2 — flow
	P2401 = Code{"P2401", Warning, "unreachable code"}
	P2402 = Code{"P2402", Error, "condition always has the same result"}
	P2403 = Code{"P2403", Warning, "dead catch"}

	// P2 — classes
	P2501 = Code{"P2501", Error, "abstract method not implemented"}
	P2502 = Code{"P2502", Error, "incompatible override"}
	P2503 = Code{"P2503", Error, "#[Override] on a member that overrides nothing"}
	P2504 = Code{"P2504", Error, "abstract method in a concrete class"}
	P2505 = Code{"P2505", Error, "final method overridden"}
	P2506 = Code{"P2506", Error, "trait method conflict"}
	P2507 = Code{"P2507", Error, "enum case value mismatch"}
	P2508 = Code{"P2508", Error, "circular inheritance"}
	P2509 = Code{"P2509", Error, "extending a final class"}
	P2510 = Code{"P2510", Error, "wrong kind of type in extends or implements"}
	P2511 = Code{"P2511", Error, "duplicate member"}
	P2512 = Code{"P2512", Error, "property hook type mismatch"}
	P2513 = Code{"P2513", Error, "readonly class rule violated"}
	P2514 = Code{"P2514", Error, "interface rule violated"}
	P2515 = Code{"P2515", Error, "uninitialised readonly property"}
	P2516 = Code{"P2516", Error, "untyped declaration"}
	P2518 = Code{"P2518", Error, "map key type outside int|string"}
	P2519 = Code{"P2519", Error, "method call on a receiver that is not one kind of value"}
	P2530 = Code{"P2530", Error, "bytes used as text"}
	P2531 = Code{"P2531", Error, "closure in a constant expression captures its scope"}

	// P26xx — concurrency (design/concurrency.md §7)
	P2601 = Code{"P2601", Error, "spawn outside of a concurrently block"}
	P2602 = Code{"P2602", Error, "task handle leaves its block"}
	P2603 = Code{"P2603", Error, "control flow crosses a concurrently or spawn block"}
	P2604 = Code{"P2604", Error, "spawn body writes to a variable of the enclosing scope"}
	P2605 = Code{"P2605", Error, "blocking sleep inside a concurrently block"}
	P2606 = Code{"P2606", Error, "bad concurrently option"}

	// P3 — generics
	P3001 = Code{"P3001", Error, "wrong number of type arguments"}
	P3002 = Code{"P3002", Error, "type argument outside its bound"}
	P3003 = Code{"P3003", Error, "erased type parameter used as a value"}
	P3004 = Code{"P3004", Error, "variance violation"}
	// P3005 and P3006 belonged to reified type parameters, removed on
	// 2026-10-29 (design/generics.md §11.2); the codes stay reserved.
	P3007 = Code{"P3007", Error, "generic property redeclared"}
	P3008 = Code{"P3008", Error, "class type parameter in a static property"}
	P3009 = Code{"P3009", Error, "type arguments on a non-generic callee"}

	// P4 — interop
	P4001 = Code{"P4001", Warning, "docblock cannot be parsed"}
	P4002 = Code{"P4002", Warning, "docblock type disagrees with the signature"}
	P4003 = Code{"P4003", Warning, "unknown template"}
	P4004 = Code{"P4004", Warning, "unknown class in a docblock"}
	P4005 = Code{"P4005", Warning, "Composer autoload maps not found"}
	P4006 = Code{"P4006", Warning, "deprecated"}
	P4007 = Code{"P4007", Warning, "PHP extensions could not be read"}
	// P4008–P4010 are about the target (design/php-versions.md §2), reported
	// on phaenon.json or composer.json.
	P4008 = Code{"P4008", Warning, "target above the PHP composer.json admits"}
	P4009 = Code{"P4009", Error, "PHP target phn does not support"}
	P4010 = Code{"P4010", Warning, "no PHP target stated"}
	// P4011: "clamp() needs PHP 8.6 (target 8.5)", a library symbol newer
	// than the target (design/php-versions.md §6).
	P4011 = Code{"P4011", Error, "library symbol needs a newer PHP than the target"}
)
