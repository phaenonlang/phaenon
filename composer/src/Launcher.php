<?php

declare(strict_types=1);

namespace Phaenon\Composer;

/**
 * vendor/bin/phn: runs the phn binary with the launcher's arguments, standard streams and exit
 * status.
 *
 * The binary is PHN_BINARY, otherwise the one the plugin installed in the package. When neither is
 * there, as after an install with --no-plugins, the launcher fetches it as the plugin would: the
 * same pinned checksum, the root composer.json's binary-url, Composer's cache.
 */
final class Launcher
{
    /**
     * The status when there is no binary to run, a shell's for a command it cannot find.
     */
    public const int UNAVAILABLE = 127;

    /**
     * The status when the binary is there but cannot be started, a shell's for that case.
     */
    public const int NOT_EXECUTABLE = 126;

    /**
     * @param list<string> $arguments    what follows the launcher's name on its command line
     * @param ?string      $autoloadPath $_composer_autoload_path, which Composer's bin proxy sets
     */
    public static function main(array $arguments, ?string $autoloadPath): int
    {
        try {
            $binary = self::binary($autoloadPath);
        } catch (\RuntimeException $e) {
            fwrite(STDERR, 'phaenon/phn: ' . $e->getMessage() . PHP_EOL);

            return self::UNAVAILABLE;
        }

        return self::run($binary, $arguments);
    }

    /**
     * @throws BinaryUnavailable
     * @throws \UnexpectedValueException when checksums.json or extra.phaenon is malformed
     */
    private static function binary(?string $autoloadPath): string
    {
        $override = BinaryResolver::override();
        if ($override !== null) {
            return $override;
        }
        // This file is composer/src/Launcher.php in the installed package.
        $packageDir = \dirname(__DIR__, 2);
        $installed = BinaryResolver::installedPath($packageDir, Platform::current());
        // The plugin checked the binary against the pinned checksum when it put it there, and
        // Composer replaces the package directory with each version. Hashing 9 MB on every run
        // would add some 45 ms to each command.
        if (is_file($installed)) {
            return $installed;
        }
        $project = RootProject::locate($autoloadPath, $packageDir);
        $resolver = new BinaryResolver(
            $packageDir,
            new StreamDownloader(Credentials::find($project->dir)),
            self::cacheDir(),
            $project->settings()->binaryUrl,
            static function (string $message): void {
                fwrite(STDERR, 'phaenon/phn: ' . $message . PHP_EOL);
            },
        );

        return $resolver->install();
    }

    /**
     * @param list<string> $arguments
     */
    private static function run(string $binary, array $arguments): int
    {
        if (PHP_OS_FAMILY !== 'Windows' && \function_exists('pcntl_exec')) {
            // phn takes over this process, its PID, terminal and signals: a Ctrl-C, or an editor
            // stopping its language server, reaches phn itself. It returns only on failure.
            pcntl_exec($binary, $arguments);
            fwrite(STDERR, sprintf(
                'phaenon/phn: cannot run %s: %s%s',
                $binary,
                pcntl_strerror(pcntl_get_last_error()),
                PHP_EOL,
            ));

            return self::NOT_EXECUTABLE;
        }
        $process = @proc_open([$binary, ...$arguments], [STDIN, STDOUT, STDERR], $pipes);
        if ($process === false) {
            fwrite(STDERR, sprintf('phaenon/phn: cannot run %s%s', $binary, PHP_EOL));

            return self::NOT_EXECUTABLE;
        }
        $status = proc_close($process);

        return $status === -1 ? 1 : $status;
    }

    /**
     * Composer's cache directory as Composer finds it when its configuration does not set
     * cache-dir. A wrong guess costs a download, never a wrong binary: every copy is verified.
     */
    private static function cacheDir(): ?string
    {
        $dir = getenv('COMPOSER_CACHE_DIR');
        if (\is_string($dir) && $dir !== '') {
            return $dir;
        }
        $home = getenv('COMPOSER_HOME');
        if (\is_string($home) && $home !== '') {
            return $home . '/cache';
        }
        if (PHP_OS_FAMILY === 'Windows') {
            $local = getenv('LOCALAPPDATA');

            return \is_string($local) && $local !== '' ? $local . '/Composer' : null;
        }
        $user = getenv('HOME');
        if (!\is_string($user) || $user === '') {
            return null;
        }
        if (PHP_OS_FAMILY === 'Darwin') {
            return $user . '/Library/Caches/composer';
        }
        $cache = getenv('XDG_CACHE_HOME');

        return (\is_string($cache) && $cache !== '' ? $cache : $user . '/.cache') . '/composer';
    }
}
