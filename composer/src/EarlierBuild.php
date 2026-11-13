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
 * installed, with this one exception: where its output directory holds a stamp, in a package
 * whose directory an earlier build can have survived in (survives()), the files no source stands
 * behind are removed, then the stamp.
 *
 * A stamp alone proves little: a file of that name can come with a release that tracks one, or
 * with the copy Composer makes of a checkout that holds one. An archive and a copied path
 * repository are installed into an emptied directory, so a stamp there came with the files, and
 * it is left alone with everything beside it. The plugin asks for one thing more before it
 * removes anything: that phn would read the package's project file (ProjectFile::$strict). A key
 * a later compiler adds may change what stands behind a file, and phn refuses such a file.
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
 * Every file removed is named: the line is printed once in a package's life, and the files were
 * untracked, so nothing else can say afterwards what went. A warning ends with what can be done.
 *
 * phn does the same (`phn build --vendor`, cmd/phn/earlier.go), and the plugin leaves it to phn
 * in a dump that builds installed packages, so that one dump says it once.
 * cmd/phn/testdata/earlier-builds.json in the compiler's repository holds the two to each other,
 * the lines they print included: wherever phn removes nothing, this class removes nothing.
 */
final readonly class EarlierBuild
{
    /**
     * @param string       $output   the output directory that held the stamp, relative to the
     *                               package, as are the paths below
     * @param list<string> $removed  the files removed, sorted
     * @param list<string> $failed   the files that could not be removed, sorted
     * @param ?string      $obstacle why nothing was removed, when nothing could be
     * @param bool         $linked   whether the package's directory is a link to a checkout
     */
    private function __construct(
        public string $output,
        public array $removed,
        public array $failed,
        public ?string $obstacle,
        public bool $linked,
    ) {
    }

    /**
     * Whether a build made in the package's directory can have outlived an upgrade of the
     * package. Composer updates an archive and a copied path repository by removing the
     * directory and installing anew, so nothing of an earlier build is ever there. A checkout of
     * the package's repository is updated in place, and a linked path repository is a checkout
     * Composer never empties. A package whose installation source is not known is not known to
     * be either.
     *
     * @param ?string $installationSource `source` or `dist`, as Composer recorded it
     */
    private static function survives(?string $installationSource, string $packageDir): bool
    {
        return $installationSource === 'source' || is_link($packageDir);
    }

    /**
     * Clears what earlier builds left in the committed roots of the package installed in
     * $packageDir, and returns what was found and done, by root. A root whose output directory
     * holds no stamp is not touched and not returned. Nothing is touched, and nothing returned,
     * for a package no earlier build can have survived in, or whose project file phn would not
     * read: `phn build --vendor` removes nothing for either.
     *
     * @param ?string $installationSource `source` or `dist`, as Composer recorded it
     *
     * @return list<self>
     */
    public static function clear(ProjectFile $file, string $packageDir, ?string $installationSource): array
    {
        if (!$file->strict || !self::survives($installationSource, $packageDir)) {
            return [];
        }
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
     * What is printed of it: a line that says what was done, then a line for each file. The text
     * is phn's (earlierBuild.report in cmd/phn/earlier.go).
     *
     * @param string $package the package's name and version
     * @param string $shown   where it is installed, as shown to the reader
     *
     * @return non-empty-list<string>
     */
    public function report(string $package, string $shown): array
    {
        $head = sprintf(
            '%s: %s/%s is the stamp of a build, in a root the package commits',
            $package,
            $this->output,
            ProjectFile::STAMP,
        );
        $remedy = $this->linked
            ? 'The package is a link to a checkout: `phn build` there removes what a build left, the stamp with it'
            : sprintf('To clear the directory, remove %s and install the package again', $shown);
        $removed = self::count(\count($this->removed), 'file with no source behind it', 'files with no source behind them');
        $again = sprintf(
            '%s could not be removed, so the stamp stays and the removal is tried again next time. %s',
            self::count(\count($this->failed), 'file', 'files'),
            $remedy,
        );
        if ($this->obstacle !== null) {
            return [$head . ', and nothing was removed: ' . $this->obstacle . '. What a build left there may still load. ' . $remedy];
        }
        if ($this->failed === [] && $this->removed === []) {
            return [$head . ': removed it, and no file, since every file there has a source'];
        }
        $lines = [match (true) {
            $this->failed === [] => $head . ': removed it and ' . $removed,
            $this->removed === [] => $head . ': ' . $again,
            default => $head . ': removed ' . $removed . ', but ' . $again,
        }];
        foreach ($this->removed as $file) {
            $lines[] = '  - removed ' . $file;
        }
        foreach ($this->failed as $file) {
            $lines[] = '  - could not remove ' . $file;
        }

        return $lines;
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
        if (!self::isStamp($packageDir . '/' . $stamp)) {
            return null;
        }
        $linked = is_link($packageDir);
        $kept = static fn (string $obstacle): self => new self($output, [], [], $obstacle, $linked);
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
        // On a file system that takes one spelling of a name for another, the stamp may have
        // gone with the files above, under the name the directory lists it by.
        clearstatcache(true);
        $gone = !is_link($packageDir . '/' . $stamp) && !file_exists($packageDir . '/' . $stamp);
        if ($failed === [] && !$gone && !self::removeRegular($packageDir . '/' . $stamp)) {
            $failed[] = $stamp;
        }

        return new self($output, $removed, $failed, null, $linked);
    }

    /**
     * Whether $path is a stamp as phn writes one: a regular file, not a link to one, whose text
     * begins with the compiler's name (`phn <version> …`).
     */
    private static function isStamp(string $path): bool
    {
        return !is_link($path) && is_file($path) && @file_get_contents($path, false, null, 0, 4) === 'phn ';
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
            // Nothing answers to the name. A link that leads nowhere PHP can follow is
            // something all the same, under whatever spelling the file system takes the name
            // for; so is any name in a directory that cannot be searched, or cannot be read.
            if (is_link($path) || !self::lacks($dir, $name)) {
                $at = $walked;

                return null;
            }

            return false;
        }

        return true;
    }

    /**
     * Whether the directory provably has no entry $name: it can be read, does not list the name,
     * and can be searched. A look at the first entry it lists shows that it can be searched;
     * where it lists none, the permission to search it is asked for. A name it does not list
     * may still be there under a spelling the file system takes for the same: the caller has
     * asked the file system for the name before it asks here.
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

        return is_executable($dir);
    }

    /**
     * Removes the file at $path when it is a regular file still, and says whether it did.
     */
    private static function removeRegular(string $path): bool
    {
        return !is_link($path) && is_file($path) && @unlink($path);
    }

    private static function count(int $n, string $one, string $many): string
    {
        return $n === 1 ? '1 ' . $one : $n . ' ' . $many;
    }
}
