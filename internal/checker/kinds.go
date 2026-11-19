package checker

// Kind says why a finding is reported (design/roadmap.md D33,
// design/checker.md §9, "Extra checks are a kind of finding").
type Kind uint8

const (
	// LanguageRule is what Phaenon's syntax and types ask for: the removed
	// constructs, text and bytes, throws, generics, declared types on every
	// declaration, and what is said of the project and of the code it is
	// compiled beside. PHPStan never sees the source and has no say in
	// them; they are always reported. It is the zero value: the findings
	// made outside the checker, of a file that does not parse and of the
	// project file, are of this kind.
	LanguageRule Kind = iota
	// Parity is a finding the compiled output draws without Phaenon: PHP
	// refuses the code, or PHPStan reports it at level max with
	// treatPhpDocTypesAsCertain off. It is always reported, and the checker
	// is never more lenient than PHPStan there.
	Parity
	// Extra is a finding PHPStan does not make at that level, kept because
	// the code can fail when it runs or a declared type says less than the
	// code relies on. Extra checks are on unless Options.NoExtraChecks.
	Extra
)

// String is the kind as `phn check --format=json` and the language server
// write it.
func (k Kind) String() string {
	switch k {
	case Parity:
		return "parity"
	case Extra:
		return "extra"
	}
	return "language"
}

// languageRules are the codes that are language rules as a whole: every
// finding of such a code is one, whichever place reports it.
var languageRules = codeSet(
	// P1: a file that does not parse, removed constructs, constructs the
	// target lacks, syntax Phaenon forbids or has not designed, and the
	// constructs PHP deprecates.
	P1000, P1001, P1002, P1003, P1004, P1005, P1006, P1007, P1101,
	// An import nothing uses. PHPStan sees the import in the output and has
	// no rule for it, and nothing can fail, so it is no extra check, which
	// has to name something that can go wrong: it is a warning of Phaenon's
	// own about the source's hygiene, and the switch leaves it alone.
	P2013,
	// Type aliases, throws and nothrow, declared types, maps, bytes.
	P2015, P2319, P2320, P2404, P2405, P2516, P2518, P2530,
	// P3: generics.
	P3001, P3002, P3003, P3004, P3007, P3008, P3009, P3010,
	// P4: interop and the project, an ignore comment that silences nothing
	// and a key of the project file in an earlier spelling among them.
	P4001, P4002, P4003, P4004, P4005, P4006, P4007, P4008, P4009, P4010, P4011, P4012,
	P4015, P4016,
)

// placedCodes are the codes whose kind is decided where the finding is
// reported: a parity finding through report, an extra check through
// reportExtra. TestEveryCodeHasAKind holds the two lists to codes.go:
// every code is in one of them, and in one only.
var placedCodes = codeSet(
	P2001, P2002, P2003, P2004, P2005, P2006, P2007, P2008, P2009, P2010, P2011, P2012, P2014,
	P2101, P2102, P2103, P2105, P2106, P2107, P2108, P2109, P2110, P2111, P2112, P2113, P2114,
	P2201, P2202, P2203, P2204, P2205, P2206, P2207, P2208, P2209, P2210, P2211, P2212,
	P2301, P2302, P2303, P2304, P2305, P2306, P2307, P2308, P2309, P2310, P2311, P2312, P2313,
	P2314, P2315, P2316, P2317, P2318, P2321,
	P2401, P2402, P2403, P2406, P2407, P2408, P2409,
	P2501, P2502, P2503, P2504, P2505, P2506, P2507, P2508, P2509, P2510, P2511, P2512, P2513,
	P2514, P2515, P2531, P2533,
)

func codeSet(codes ...Code) map[string]bool {
	set := make(map[string]bool, len(codes))
	for _, c := range codes {
		set[c.ID] = true
	}
	return set
}

// IsLanguageRule reports whether every finding of the code is a language
// rule.
func IsLanguageRule(code string) bool { return languageRules[code] }
