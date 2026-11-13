<?php

declare(strict_types=1);

namespace Phaenon\Composer;

/**
 * An installed package's phaenon.json as the plugin reads it: `roots` and `committed`, whatever
 * other keys the file has (design/tooling.md §3.7, T18).
 *
 * `{"roots": {"src": ".src", "tests": ".tests"}, "committed": ["src"]}` is a library that commits
 * the outputs of `src`: where it is installed, that root is left as installed and never built, and
 * it must have come with its outputs. The plugin asks whether they are there before it fetches a
 * compiler, which is why it reads the file itself; phn stays the reader of everything else in it.
 *
 * The two readers give one answer for the two keys. cmd/phn/testdata/project-files.json in the
 * compiler's repository is the reference both are tested against, and phn makes the same check of
 * presence, in the same words (`phn build --vendor`, cmd/phn/vendor.go).
 */
final readonly class ProjectFile
{
    /**
     * The file phn leaves in each output directory it builds in an installed package.
     */
    public const string STAMP = '.phn-build';

    /**
     * @param array<string, string> $roots     each source root with its output directory, as
     *                                         cleaned paths relative to the package
     * @param list<string>          $committed the roots whose outputs the package commits, each a
     *                                         key of $roots
     */
    private function __construct(
        public array $roots,
        public array $committed,
    ) {
    }

    /**
     * @return ?self null when there is no file to read or phn would refuse its two keys: phn then
     *               says what is wrong with it
     */
    public static function read(string $path): ?self
    {
        $json = @file_get_contents($path);

        return $json === false ? null : self::parse($json);
    }

    /**
     * @return ?self null when phn would refuse the file's `roots` or `committed`
     */
    public static function parse(string $json): ?self
    {
        // Objects stay objects, so that a list where phn wants an object is told from one.
        $data = json_decode($json);
        if (!$data instanceof \stdClass) {
            return null;
        }
        // A key is spelt as documented: phn refuses "Roots" and "COMMITTED".
        foreach (array_keys(get_object_vars($data)) as $key) {
            foreach (['roots', 'committed'] as $name) {
                if ((string) $key !== $name && strcasecmp((string) $key, $name) === 0) {
                    return null;
                }
            }
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

        return new self($roots, $committed);
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
     * The output directories of committed roots that hold the stamp of a build (`.phn-build`),
     * as paths relative to the package. No build writes one there, and a package's repository
     * ignores the file: it is what a build of an earlier release left behind, when the root was
     * built where the package is installed, and the outputs of that build may lie beside it.
     *
     * @return list<string>
     */
    public function builtBefore(string $packageDir): array
    {
        $outputs = [];
        foreach ($this->committed as $source) {
            if (is_file($packageDir . '/' . $this->roots[$source] . '/' . self::STAMP)) {
                $outputs[] = $this->roots[$source];
            }
        }
        sort($outputs, \SORT_STRING);

        return $outputs;
    }

    /**
     * The .phn files phn compiles under a directory of a root, as paths relative to the package:
     * not those of a nested vendor directory, and a symbolic link is not followed.
     *
     * @return ?list<string> null when a directory cannot be read
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
