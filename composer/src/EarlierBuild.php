<?php

declare(strict_types=1);

namespace Phaenon\Composer;

/**
 * What a build of an earlier release left in a root an installed package now commits, found and
 * removed (design/tooling.md §3.7, T18).
 *
 * A package that shipped its sources alone was built where it is installed. When a later release
 * commits the outputs of a root and the upgrade keeps the directory, as the git checkout of a
 * source install does, what that build wrote stays beside the committed files: its stamp, and the
 * outputs of the sources the release dropped, which still load. A committed root is left as
 * installed, with this one exception: where its output directory holds the stamp, the files no
 * source stands behind are removed, then the stamp. The stamp is the proof that the root was
 * built here: only `phn build --vendor` writes one, a library's repository ignores the file, and
 * no build writes one into a committed root.
 *
 * What is removed is what phn's build removes from an output directory (mirror and sourcesBehind
 * in cmd/phn/project.go): a file whose path does not exist under the root, nor, for a `.php`, the
 * `.phn` it would be compiled from. And only when that is sure:
 *
 * - A file goes when its absence from the root is proved: the name is missing from a directory
 *   that can be read and searched, or the path runs through a file. A directory that cannot be
 *   read and a link that leads nowhere prove nothing, and then nothing is removed from the root
 *   at all: with unreadable sources every output would look like an orphan.
 * - Only regular files go, and the directories their removal empties. A symbolic link in the
 *   output directory is neither followed nor removed: a build writes none.
 * - The root and its output directory are directories of the package: if either is a symbolic
 *   link or lies behind one, nothing is removed. A link inside a package may lead anywhere, into
 *   another package's outputs for one. The package's own directory may be a link, a path
 *   repository's: the files removed there are the ones `phn build` in that checkout removes.
 * - The whole list is made before the first file goes, and the stamp goes last, so that a
 *   removal that fails leaves the stamp for the next dump.
 *
 * phn does the same when run by hand (`phn build --vendor`, cmd/phn/earlier.go);
 * cmd/phn/testdata/earlier-builds.json in the compiler's repository holds the two to each other,
 * the lines they print included.
 */
final readonly class EarlierBuild
{
    /**
     * @param string       $output   the output directory that held the stamp, relative to the
     *                               package, as are the paths below
     * @param list<string> $removed  the files removed, sorted
     * @param list<string> $failed   the files that could not be removed, sorted
     * @param ?string      $obstacle why nothing was removed, when nothing could be
     */
    private function __construct(
        public string $output,
        public array $removed,
        public array $failed,
        public ?string $obstacle,
    ) {
    }

    /**
     * Clears what earlier builds left in the committed roots of the package installed in
     * $packageDir, and returns what was found and done, by root. A root whose output directory
     * holds no stamp is not touched and not returned.
     *
     * @return list<self>
     */
    public static function clear(ProjectFile $file, string $packageDir): array
    {
        clearstatcache(true);
        $committed = $file->committed;
        sort($committed, \SORT_STRING);
        $found = [];
        foreach ($committed as $source) {
            $build = self::clearRoot($packageDir, $source, $file->roots[$source]);
            if ($build !== null) {
                $found[] = $build;
            }
        }

        return $found;
    }

    /**
     * Whether something is left that the next dump will find again.
     */
    public function isWarning(): bool
    {
        return $this->obstacle !== null || $this->failed !== [];
    }

    /**
     * The line that says what was done. Its text is phn's (earlierBuild.report in
     * cmd/phn/earlier.go).
     *
     * @param string $package the package's name and version
     */
    public function report(string $package): string
    {
        $head = sprintf(
            '%s: %s/%s is the stamp of an earlier build of this package, in a root it now commits',
            $package,
            $this->output,
            ProjectFile::STAMP,
        );
        $removed = match (true) {
            $this->removed === [] => '',
            \count($this->removed) === 1 => sprintf('removed 1 file with no source behind it (%s)', $this->removed[0]),
            default => sprintf('removed %d files with no source behind them (%s)', \count($this->removed), self::listed($this->removed)),
        };
        if ($this->obstacle !== null) {
            return $head . ', and nothing was removed: ' . $this->obstacle . '. What that build left there may still load';
        }
        if ($this->failed !== []) {
            return $head . ': ' . ($removed === '' ? '' : $removed . ', but ') . self::listed($this->failed)
                . ' could not be removed, so the stamp stays and the next build tries again';
        }

        return $removed === ''
            ? $head . ': removed the stamp alone, since every file there has a source'
            : $head . ': ' . $removed . ' and the stamp';
    }

    /**
     * @param string $source the root, relative to the package
     * @param string $output its output directory, likewise
     *
     * @return ?self null when the output directory holds no stamp, and nothing was touched
     */
    private static function clearRoot(string $packageDir, string $source, string $output): ?self
    {
        $stamp = $output . '/' . ProjectFile::STAMP;
        if (is_link($packageDir . '/' . $stamp) || !is_file($packageDir . '/' . $stamp)) {
            return null;
        }
        $kept = static fn (string $obstacle): self => new self($output, [], [], $obstacle);
        // Under open_basedir a path PHP refuses to look at reads as one that does not exist.
        if ((string) \ini_get('open_basedir') !== '') {
            return $kept('PHP runs with open_basedir, under which a source it may not read looks like one that is gone');
        }
        foreach ([$source, $output] as $root) {
            $link = self::linkOnPath($packageDir, $root);
            if ($link !== null) {
                return $kept($link . ' is a symbolic link');
            }
        }
        $root = $packageDir . '/' . $source;
        if (!is_dir($root)) {
            return $kept(file_exists($root) ? $source . ' cannot be read' : 'the package is installed without ' . $source);
        }
        $files = [];
        $unread = self::regularFiles($packageDir, $output, $files);
        if ($unread !== null) {
            return $kept($unread . ' cannot be read');
        }
        $orphans = [];
        foreach ($files as $file) {
            $relative = substr($file, \strlen($output) + 1);
            if ($relative === ProjectFile::STAMP) {
                continue;
            }
            // phn's sourcesBehind: the .phn a .php is compiled from, and the file of the same path.
            $behind = str_ends_with($relative, '.php') ? [substr($relative, 0, -\strlen('.php')) . '.phn', $relative] : [$relative];
            $state = false;
            $unknown = null;
            foreach ($behind as $candidate) {
                $at = null;
                $state = self::source($root, $candidate, $at);
                if ($state === true) {
                    break;
                }
                if ($state === null) {
                    $unknown ??= $at;
                }
            }
            if ($state === true) {
                continue;
            }
            if ($unknown !== null) {
                return $kept('whether ' . $source . '/' . $unknown . ' exists cannot be told');
            }
            $orphans[] = $file;
        }
        // From here on files are removed: the list above is whole.
        sort($orphans, \SORT_STRING);
        $removed = [];
        $failed = [];
        foreach ($orphans as $file) {
            if (!self::removeRegular($packageDir . '/' . $file)) {
                $failed[] = $file;

                continue;
            }
            $removed[] = $file;
            // The directories this emptied, never the output directory itself.
            for ($dir = \dirname($file); $dir !== $output; $dir = \dirname($dir)) {
                $full = $packageDir . '/' . $dir;
                if (is_link($full) || !is_dir($full) || !@rmdir($full)) {
                    break;
                }
            }
        }
        if ($failed === [] && !self::removeRegular($packageDir . '/' . $stamp)) {
            $failed[] = $stamp;
        }

        return new self($output, $removed, $failed, null);
    }

    /**
     * The first directory on the path $relative under $dir that is a symbolic link, the path
     * itself included.
     */
    private static function linkOnPath(string $dir, string $relative): ?string
    {
        $path = '';
        foreach (explode('/', $relative) as $name) {
            $path = $path === '' ? $name : $path . '/' . $name;
            if (is_link($dir . '/' . $path)) {
                return $path;
            }
        }

        return null;
    }

    /**
     * Adds to $files the regular files under the directory $dir of the package, as paths that
     * start with $dir. A symbolic link is passed over, whatever it leads to.
     *
     * @param list<string> $files
     *
     * @return ?string the directory that cannot be read, when one cannot
     */
    private static function regularFiles(string $packageDir, string $dir, array &$files): ?string
    {
        $names = @scandir($packageDir . '/' . $dir);
        if ($names === false) {
            return $dir;
        }
        foreach ($names as $name) {
            if ($name === '.' || $name === '..') {
                continue;
            }
            $path = $dir . '/' . $name;
            $full = $packageDir . '/' . $path;
            if (is_link($full)) {
                continue;
            }
            if (is_dir($full)) {
                $unread = self::regularFiles($packageDir, $path, $files);
                if ($unread !== null) {
                    return $unread;
                }
            } elseif (is_file($full)) {
                $files[] = $path;
            } elseif (!file_exists($full)) {
                return $dir; // a name it lists and cannot look at: the directory cannot be searched
            }
        }

        return null;
    }

    /**
     * Whether the path $relative exists under $root, as phn's build asks it (a stat, which
     * follows links), and, when nothing answers, whether that is because nothing is there.
     *
     * @param ?string $at the path, relative to $root, that could not be examined, when the
     *                    answer is null
     *
     * @return ?bool true when it exists; false when its absence is proved: the name is missing
     *               from a directory that can be read and searched, or the path runs through a
     *               file; null when neither is known
     */
    private static function source(string $root, string $relative, ?string &$at): ?bool
    {
        $at = null;
        if (file_exists($root . '/' . $relative)) {
            return true;
        }
        $dir = $root;
        $walked = '';
        $names = explode('/', $relative);
        foreach ($names as $i => $name) {
            $walked = $walked === '' ? $name : $walked . '/' . $name;
            $path = $dir . '/' . $name;
            if (is_dir($path)) {
                $dir = $path;

                continue;
            }
            if (file_exists($path)) {
                // The last name has appeared since; any other is a file the path runs through.
                return $i === \count($names) - 1;
            }
            // Nothing answers to the name. A name the directory lists is something all the
            // same, a link that leads nowhere PHP can follow; so is any name in a directory
            // that cannot be searched, or cannot be read.
            if (!self::lacks($dir, $name)) {
                $at = $walked;

                return null;
            }

            return false;
        }

        return true;
    }

    /**
     * Whether the directory provably has no entry $name: it can be read, does not list the name,
     * and can be searched, which a look at the first entry it lists shows. Without the search a
     * name could be there under another spelling the file system takes for the same.
     */
    private static function lacks(string $dir, string $name): bool
    {
        $names = @scandir($dir);
        if ($names === false || \in_array($name, $names, true)) {
            return false;
        }
        foreach ($names as $listed) {
            if ($listed !== '.' && $listed !== '..') {
                return is_link($dir . '/' . $listed) || file_exists($dir . '/' . $listed);
            }
        }

        return true;
    }

    /**
     * Removes the file at $path when it is a regular file still, and says whether it did.
     */
    private static function removeRegular(string $path): bool
    {
        return !is_link($path) && is_file($path) && @unlink($path);
    }

    /**
     * @param non-empty-list<string> $paths
     */
    private static function listed(array $paths): string
    {
        return \count($paths) > 1 ? sprintf('%s, and %d more', $paths[0], \count($paths) - 1) : $paths[0];
    }
}
