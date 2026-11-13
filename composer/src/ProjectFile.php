<?php

declare(strict_types=1);

namespace Phaenon\Composer;

/**
 * An installed package's phaenon.json as the plugin reads it: `roots` and `committed`
 * (design/tooling.md §3.7, T18).
 *
 * `{"roots": {"src": ".src", "tests": ".tests"}, "committed": ["src"]}` is a library that commits
 * the outputs of `src`: where it is installed, that root is left as installed and never built, and
 * it must have come with its outputs. The plugin asks whether they are there before it fetches a
 * compiler, which is why it reads the file itself; phn stays the reader of everything else in it.
 *
 * The plugin reads the file as phn reads an installed package's (readInstalledProject in
 * cmd/phn/project.go; design/tooling.md T19): a key of the file's object that it does not know
 * is passed over and remembered ($unknown), since the project that installed the package cannot
 * change its file; everything else phn refuses there, the plugin refuses, among it a known key
 * with a value it cannot have and a key written twice. cmd/phn/testdata/project-files.json in the
 * compiler's repository is the reference both are tested against, and phn makes the same check of
 * presence, in the same words (`phn build --vendor`, cmd/phn/vendor.go).
 */
final readonly class ProjectFile
{
    /**
     * The file phn leaves in each output directory it builds in an installed package. In a
     * committed root, which no build writes one into, it is what a build of an earlier release
     * left (EarlierBuild).
     */
    public const string STAMP = '.phn-build';

    /**
     * @param array<string, string> $roots     each source root with its output directory, as
     *                                         cleaned paths relative to the package
     * @param list<string>          $committed the roots whose outputs the package commits, each a
     *                                         key of $roots
     * @param list<string>          $unknown   the keys of the file that this reader does not
     *                                         know and has passed over, sorted. With none, phn
     *                                         would read the file as a project's own as well.
     *                                         The package is checked and built whatever they
     *                                         are; nothing is removed from it while there is one
     *                                         (EarlierBuild)
     */
    private function __construct(
        public array $roots,
        public array $committed,
        public array $unknown,
    ) {
    }

    /**
     * The keys phn knows in a project file, and the one under `unused`.
     */
    private const array KEYS = ['roots', 'committed', 'exclude', 'php', 'fmt', 'unused'];
    private const string UNUSED_KEY = 'reflectedAttributes';

    /**
     * @return ?self null when there is no file to read or phn would refuse it as an installed
     *               package's: phn then says what is wrong with it
     */
    public static function read(string $path): ?self
    {
        $json = @file_get_contents($path);

        return $json === false ? null : self::parse($json);
    }

    /**
     * @return ?self null when phn would refuse the file as an installed package's
     */
    public static function parse(string $json): ?self
    {
        // Objects stay objects, so that a list where phn wants an object is told from one.
        $data = json_decode($json);
        if (!$data instanceof \stdClass || self::repeatsAKey($json)) {
            return null;
        }
        // A key phn knows is spelt as documented: it refuses "Roots" and "COMMITTED". Any
        // other key is one it does not know, and is passed over.
        $unknown = [];
        foreach (array_keys(get_object_vars($data)) as $key) {
            $key = (string) $key;
            $named = self::names($key, self::KEYS);
            if ($named === null) {
                $unknown[] = $key;
            } elseif ($named !== $key) {
                return null;
            }
        }
        sort($unknown, \SORT_STRING);
        if (!self::readsTheOtherKeys($data)) {
            return null;
        }
        $roots = [];
        $given = $data->roots ?? new \stdClass();
        if (!$given instanceof \stdClass) {
            return null;
        }
        foreach (get_object_vars($given) as $source => $output) {
            if (!\is_string($output)) {
                return null;
            }
            $source = self::clean((string) $source);
            $output = self::clean($output);
            // A root and its output are directories inside the package, and siblings: phn's rules
            // (cmd/phn/project.go).
            if (
                self::leavesThePackage($source) || self::leavesThePackage($output)
                || $source === $output || \dirname($source) !== \dirname($output)
            ) {
                return null;
            }
            // Two spellings of one root ("src", "./src") are one root when they name one output.
            if (($roots[$source] ?? $output) !== $output) {
                return null;
            }
            $roots[$source] = $output;
        }
        if (!self::separate($roots)) {
            return null;
        }
        $committed = [];
        $listed = $data->committed ?? [];
        if (!\is_array($listed)) {
            return null;
        }
        foreach ($listed as $entry) {
            if (!\is_string($entry)) {
                return null;
            }
            $source = self::clean($entry);
            if (!isset($roots[$source])) {
                return null;
            }
            if (!\in_array($source, $committed, true)) {
                $committed[] = $source;
            }
        }

        return new self($roots, $committed, $unknown);
    }

    /**
     * The key of $known that $key names, in whatever letter case, as phn's decoder matches a
     * key to a field: the long s and the Kelvin sign count as s and k.
     *
     * @param list<string> $known
     */
    private static function names(string $key, array $known): ?string
    {
        $folded = strtr($key, ["\u{017F}" => 's', "\u{212A}" => 'k']);
        foreach ($known as $name) {
            if (strcasecmp($folded, $name) === 0) {
                return $name;
            }
        }

        return null;
    }

    /**
     * Whether phn reads the keys it knows beside `roots` and `committed` (readProjectFile in
     * cmd/phn/project.go): `exclude` and `fmt` lists of strings, `php` a string, `unused` an
     * object whose one key, `reflectedAttributes`, lists class names, each of them or null. A
     * key under `unused` that phn does not know is refused, not passed over: only a key of the
     * file's own object is.
     */
    private static function readsTheOtherKeys(\stdClass $data): bool
    {
        $strings = static function (mixed $list): bool {
            return $list === null || (\is_array($list) && array_is_list($list) && array_filter($list, is_string(...)) === $list);
        };
        foreach (get_object_vars($data) as $key => $value) {
            $read = match ((string) $key) {
                'exclude', 'fmt' => $strings($value),
                'php' => $value === null || \is_string($value),
                'unused' => $value === null || ($value instanceof \stdClass && self::readsUnused($value)),
                default => true, // parse() reads roots and committed, and passes over the rest
            };
            if (!$read) {
                return false;
            }
        }

        return true;
    }

    private static function readsUnused(\stdClass $unused): bool
    {
        foreach (get_object_vars($unused) as $key => $value) {
            if ((string) $key !== self::UNUSED_KEY) {
                return false;
            }
            if ($value === null) {
                continue;
            }
            if (!\is_array($value) || !array_is_list($value)) {
                return false;
            }
            foreach ($value as $name) {
                // A full class name, with or without the backslash before it.
                $identifier = '[A-Za-z_\x80-\xff][A-Za-z0-9_\x80-\xff]*';
                if (!\is_string($name) || preg_match('/^\\\\?' . $identifier . '(\\\\' . $identifier . ')*$/D', $name) !== 1) {
                    return false;
                }
            }
        }

        return true;
    }

    /**
     * Whether an object of the JSON text, which json_decode has accepted, has a key twice, at any
     * depth. phn refuses such a file: a decoder keeps the later of the two or merges them, by
     * rules of its own.
     */
    private static function repeatsAKey(string $json): bool
    {
        /** @var list<?array<string, true>> $open the keys of each open object; null for an array */
        $open = [];
        $length = \strlen($json);
        for ($i = 0; $i < $length; ++$i) {
            $char = $json[$i];
            if ($char === '{') {
                $open[] = [];
            } elseif ($char === '[') {
                $open[] = null;
            } elseif ($char === '}' || $char === ']') {
                array_pop($open);
            } elseif ($char === '"') {
                $start = $i;
                for (++$i; $i < $length && $json[$i] !== '"'; ++$i) {
                    if ($json[$i] === '\\') {
                        ++$i;
                    }
                }
                // A string is a key when a colon follows it.
                $after = $i + 1 + strspn($json, " \t\r\n", $i + 1);
                $top = array_key_last($open);
                if ($top === null || $open[$top] === null || ($json[$after] ?? '') !== ':') {
                    continue;
                }
                $key = 'k' . var_export(json_decode(substr($json, $start, $i - $start + 1)), true);
                if (isset($open[$top][$key])) {
                    return true;
                }
                $open[$top][$key] = true;
            }
        }

        return false;
    }

    /**
     * Whether the package, installed in $packageDir, has nothing for phn to build: it commits
     * outputs, and every root the install has is one of those. A dist archive leaves out the roots
     * it does not commit (`tests/`).
     */
    public function leavesNothingToBuild(string $packageDir): bool
    {
        if ($this->committed === []) {
            return false;
        }
        foreach (array_keys($this->roots) as $source) {
            // PHP turns a key such as "2024" into an integer.
            $source = (string) $source;
            if (!\in_array($source, $this->committed, true) && is_dir($packageDir . '/' . $source)) {
                return false;
            }
        }

        return true;
    }

    /**
     * The errors of a package that came without outputs it commits: for each committed root the
     * install has, in the order of their names, no output directory, or a .phn with no .php there.
     * The check is of presence only; whether an output is current is for the package's own gate to
     * say.
     *
     * The error's text and summary()'s are phn's (committedGap and committedGapsSummary in
     * cmd/phn/vendor.go): cmd/phn/testdata/committed-outputs.json holds both to them, in full.
     *
     * @param string $package    its name
     * @param string $version    its version as Composer shows it, which names the broken release
     * @param string $packageDir where it is installed
     *
     * @return ?list<string> an error for each such root; null when a directory of a committed
     *                       root cannot be read, which is phn's to report
     */
    public function missingOutputs(string $package, string $version, string $packageDir): ?array
    {
        $committed = $this->committed;
        sort($committed, \SORT_STRING);
        $errors = [];
        foreach ($committed as $source) {
            if (!is_dir($packageDir . '/' . $source)) {
                continue; // the install does not have the root
            }
            $output = $this->roots[$source];
            $missing = [];
            if (!is_dir($packageDir . '/' . $output)) {
                $missing[] = $output;
            } else {
                // phn does not walk into a root that is a symbolic link, and compiles nothing there.
                $sources = is_link($packageDir . '/' . $source) ? [] : self::sources($packageDir, $source);
                if ($sources === null) {
                    return null;
                }
                foreach ($sources as $file) {
                    $compiled = $output . substr($file, \strlen($source), -\strlen('.phn')) . '.php';
                    if (!is_file($packageDir . '/' . $compiled)) {
                        $missing[] = $compiled;
                    }
                }
                sort($missing, \SORT_STRING);
            }
            if ($missing !== []) {
                $errors[] = sprintf(
                    '%s: its phaenon.json commits the outputs of the root %s, but this release is installed without them: %s is missing%s',
                    trim($package . ' ' . $version),
                    $source,
                    $missing[0],
                    \count($missing) > 1 ? sprintf(', and %d more', \count($missing) - 1) : '',
                );
            }
        }

        return $errors;
    }

    /**
     * The line that ends the errors of $packages packages: what the failure means for the project
     * that installed them, which cannot repair another package's release, and what it can do.
     */
    public static function summary(int $packages): string
    {
        return sprintf(
            '%d installed packages lack outputs they commit, and nothing was built: '
            . 'a build here would hide a release that is broken for every project without phn. '
            . 'This project cannot repair another package\'s release: report it to that package\'s maintainers, '
            . 'or require a version of the package that has its outputs',
            $packages,
        );
    }

    /**
     * The .phn files phn compiles under a directory of a root, as paths relative to the package:
     * not those of a nested vendor directory, and a symbolic link is not followed.
     *
     * @return ?list<string> null when a directory cannot be read, or cannot be searched
     */
    private static function sources(string $packageDir, string $dir): ?array
    {
        $names = @scandir($packageDir . '/' . $dir);
        if ($names === false) {
            return null;
        }
        $files = [];
        foreach ($names as $name) {
            if ($name === '.' || $name === '..') {
                continue;
            }
            $path = $dir . '/' . $name;
            $full = $packageDir . '/' . $path;
            if (!is_link($full) && !file_exists($full)) {
                // A name the directory lists and nothing answers to: the directory can be read
                // and not searched, and what it holds, a directory of sources perhaps, is not
                // known.
                return null;
            }
            if (!is_link($full) && is_dir($full)) {
                $nested = $name === 'vendor' ? [] : self::sources($packageDir, $path);
                if ($nested === null) {
                    return null;
                }
                $files = [...$files, ...$nested];
            } elseif (str_ends_with($name, '.phn')) {
                $files[] = $path;
            }
        }

        return $files;
    }

    /**
     * Whether the roots and their outputs are separate directories, none of them another or inside
     * another: with a root inside a root, or two roots compiling into one directory, a source has
     * two places to compile into, and phn refuses the file.
     *
     * @param array<string, string> $roots
     */
    private static function separate(array $roots): bool
    {
        $dirs = [...array_map(strval(...), array_keys($roots)), ...array_values($roots)];
        foreach ($dirs as $i => $dir) {
            foreach ($dirs as $j => $other) {
                if ($i !== $j && ($dir === $other || str_starts_with($dir, $other . '/'))) {
                    return false;
                }
            }
        }

        return true;
    }

    /**
     * Whether a cleaned path is the package's directory, absolute, or outside the package.
     */
    private static function leavesThePackage(string $path): bool
    {
        return $path === '.' || $path === '..' || str_starts_with($path, '/') || str_starts_with($path, '../');
    }

    /**
     * A path as Go's filepath.Clean leaves it, the form phn compares roots in: no empty or `.`
     * elements, no trailing slash, `..` resolved against the element before it.
     */
    private static function clean(string $path): string
    {
        $absolute = str_starts_with($path, '/');
        $elements = [];
        foreach (explode('/', $path) as $element) {
            if ($element === '' || $element === '.') {
                continue;
            }
            if ($element !== '..') {
                $elements[] = $element;
            } elseif ($elements !== [] && end($elements) !== '..') {
                array_pop($elements);
            } elseif (!$absolute) {
                $elements[] = '..';
            }
        }
        $clean = implode('/', $elements);
        if ($absolute) {
            return '/' . $clean;
        }

        return $clean === '' ? '.' : $clean;
    }
}
