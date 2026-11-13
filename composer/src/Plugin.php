<?php

declare(strict_types=1);

namespace Phaenon\Composer;

use Composer\Composer;
use Composer\EventDispatcher\EventSubscriberInterface;
use Composer\EventDispatcher\ScriptExecutionException;
use Composer\IO\IOInterface;
use Composer\Plugin\PluginInterface;
use Composer\Script\Event;
use Composer\Script\ScriptEvents;
use Composer\Util\Platform as ComposerPlatform;
use Composer\Util\ProcessExecutor;

/**
 * Compiles a project's Phaenon code when Composer writes the autoloader (design/tooling.md §3.3):
 * every installed package that has a phaenon.json, then the root project if it has one, so that
 * the autoloader, `--classmap-authoritative` included, is written over outputs that exist.
 *
 * An installed package may commit the outputs of some roots (its phaenon.json's `committed`,
 * design/tooling.md §3.7, T18): those are left as installed, but for what a build of an earlier
 * release left in them, which is removed (EarlierBuild). A package whose installed roots are all
 * committed is nothing to build, and one that came without the outputs it commits fails the dump
 * before any binary is fetched and before anything is removed, as a failed build does. Only a
 * package says so, about itself: the root project is built whatever it commits.
 *
 * A project with nothing to build, its own or installed, needs no binary, and the plugin does
 * nothing. An `install --no-autoloader` builds at the `dump-autoload` that follows. Under
 * `--no-plugins` nothing here runs, and vendor/bin/phn fetches the binary when it is first used.
 */
final class Plugin implements PluginInterface, EventSubscriberInterface
{
    /**
     * The environment variable `phn build --vendor` is run with by the plugin, which has by then
     * removed what earlier builds left in committed roots: phn, seeing it, leaves those roots
     * alone. It is the plugin's word to phn and no setting of a project; a phn from before it
     * ignores it, and has no such pass to skip.
     */
    public const string EARLIER_BUILDS_CLEARED = 'PHN_EARLIER_BUILDS_CLEARED';

    public function activate(Composer $composer, IOInterface $io): void
    {
    }

    public function deactivate(Composer $composer, IOInterface $io): void
    {
    }

    public function uninstall(Composer $composer, IOInterface $io): void
    {
    }

    /**
     * @return array<string, string>
     */
    public static function getSubscribedEvents(): array
    {
        return [ScriptEvents::PRE_AUTOLOAD_DUMP => 'build'];
    }

    /**
     * @throws ScriptExecutionException when there is no binary to build with or a build fails
     */
    public function build(Event $event): void
    {
        $composer = $event->getComposer();
        $io = $event->getIO();
        try {
            $settings = Settings::fromExtra($composer->getPackage()->getExtra());
        } catch (\UnexpectedValueException $e) {
            throw self::failure($io, $e->getMessage(), 1, $e);
        }
        // Composer runs in the root project's directory (--working-dir moves it there), the
        // directory its autoload paths are relative to.
        $projectDir = ComposerPlatform::getCwd();
        $packages = [];
        $committing = [];
        $incomplete = 0;
        foreach (self::phaenonPackages($composer) as $name => [$version, $dir, $installationSource]) {
            // A file this reader refuses, or a root it cannot read, is phn's to report: the
            // package is built as before.
            $file = ProjectFile::read($dir . '/phaenon.json');
            $errors = $file?->missingOutputs($name, $version, $dir);
            if ($file === null || $errors === null) {
                $packages[] = $name;

                continue;
            }
            foreach ($errors as $error) {
                self::error($io, $error);
            }
            if ($errors !== []) {
                ++$incomplete;

                continue;
            }
            if ($file->committed !== []) {
                $committing[trim($name . ' ' . $version)] = [$file, $dir, $installationSource];
            }
            if (!$file->leavesNothingToBuild($dir)) {
                $packages[] = $name;
            }
        }
        if ($incomplete > 0) {
            throw self::failure($io, ProjectFile::summary($incomplete), 1);
        }
        // Then, and only when every package came whole, what an earlier build left in a root its
        // package now commits is removed, in the packages an earlier build can have survived in
        // (EarlierBuild). It is done here, in every dump and before a binary is resolved: it
        // takes no compiler, and so does not depend on which one the project has. A phn older
        // than the plugin, PHN_BINARY's or a checkout's own build, has no such pass.
        foreach ($committing as $package => [$file, $dir, $installationSource]) {
            foreach (EarlierBuild::clear($file, $dir, $installationSource, $projectDir) as $build) {
                self::reportAnEarlierBuild($io, $build, (string) $package, self::shown($dir, $projectDir));
            }
        }
        $buildsProject = $settings->buildRoot && is_file($projectDir . '/phaenon.json');
        if ($packages === [] && !$buildsProject) {
            $io->writeError(
                'phaenon/phn: nothing to build: no installed package has a root to build, and the project builds none',
                true,
                IOInterface::VERBOSE,
            );

            return;
        }

        try {
            $binary = self::resolver($composer, $io, $settings)->resolve();
        } catch (BinaryUnavailable | \UnexpectedValueException $e) {
            throw self::failure($io, $e->getMessage(), 1, $e);
        }
        $io->writeError(sprintf(
            'phaenon/phn: building %s with %s',
            implode(', ', $buildsProject ? [...$packages, 'the project'] : $packages),
            $binary,
        ), true, IOInterface::VERBOSE);

        AutoloadMaps::write($composer, $event->isDevMode());

        $build = new Build($binary, $projectDir, new ProcessExecutor($io), $io);
        try {
            if ($packages !== []) {
                $build->run(
                    ['build', '--vendor'],
                    'an installed package\'s output could not be written, as phn\'s messages above say '
                    . '(the findings of installed packages are only warnings)',
                    // phn has the same pass, for when it is run by hand: here it would say a
                    // second time what could not be removed.
                    [self::EARLIER_BUILDS_CLEARED => '1'],
                );
            }
            if ($buildsProject) {
                $build->run(
                    ['build'],
                    'the project does not build, as phn\'s messages above say. '
                    . 'extra.phaenon.build: false in composer.json leaves the build to the project\'s own workflow',
                );
            }
        } catch (BuildFailed $e) {
            throw self::failure($io, $e->getMessage(), $e->getCode(), $e);
        }
    }

    /**
     * The installed packages that have a phaenon.json, the ones `phn build --vendor` reads, in
     * the order it reads them: by name.
     *
     * @return array<string, array{string, string, ?string}> by its name, the version of each as
     *                                                       Composer shows it, where it is
     *                                                       installed, and what it was
     *                                                       installed from (`source`, `dist`)
     */
    private static function phaenonPackages(Composer $composer): array
    {
        $installationManager = $composer->getInstallationManager();
        $packages = [];
        foreach ($composer->getRepositoryManager()->getLocalRepository()->getCanonicalPackages() as $package) {
            $path = $installationManager->getInstallPath($package);
            if ($path !== null && is_file($path . '/phaenon.json')) {
                $packages[$package->getPrettyName()] = [$package->getPrettyVersion(), $path, $package->getInstallationSource()];
            }
        }
        ksort($packages, \SORT_STRING);

        return $packages;
    }

    /**
     * A committed root that holds the stamp of a build, in a package an earlier build can have
     * survived in, was built here by a release that shipped its sources alone, and an upgrade
     * that kept the directory left that build's outputs in it: a class the new release dropped
     * still loads. EarlierBuild has removed them and the stamp. What it did is said once, every
     * file named, since nothing can tell afterwards which files went; the next dump finds no
     * stamp and says nothing. Where it could not, the lines are a warning that says what to do,
     * and come again until the directory is put right. Under --quiet nothing of this is printed.
     */
    private static function reportAnEarlierBuild(IOInterface $io, EarlierBuild $build, string $package, string $shown): void
    {
        $lines = $build->report($package, $shown);
        $lines[0] = 'phaenon/phn: ' . $lines[0];
        foreach ($lines as $line) {
            $line = str_replace('<', '\\<', $line);
            $io->writeError($build->isWarning() ? '<warning>' . $line . '</warning>' : $line);
        }
    }

    /**
     * A directory as a message shows it: relative to the project when it lies inside it.
     */
    private static function shown(string $dir, string $projectDir): string
    {
        return str_starts_with($dir, $projectDir . '/') ? substr($dir, \strlen($projectDir) + 1) : $dir;
    }

    private static function resolver(Composer $composer, IOInterface $io, Settings $settings): BinaryResolver
    {
        $cacheDir = $composer->getConfig()->get('cache-dir');

        return new BinaryResolver(
            // This file is composer/src/Plugin.php in the installed package.
            \dirname(__DIR__, 2),
            new ComposerDownloader($composer->getLoop()->getHttpDownloader()),
            \is_string($cacheDir) ? $cacheDir : null,
            $settings->binaryUrl,
            static function (string $message) use ($io): void {
                $io->writeError('  - ' . $message);
            },
        );
    }

    /**
     * The plugin fails the command as a failing script does: Composer exits with the status and
     * prints nothing, so the message is printed here, on one line. Composer's box around an
     * exception's message would break checksums and paths across lines.
     */
    private static function failure(IOInterface $io, string $message, int $status, ?\Throwable $previous = null): ScriptExecutionException
    {
        self::error($io, $message);

        return new ScriptExecutionException($message, $status, $previous);
    }

    /**
     * An error is printed under --quiet too, where Composer prints its own: the status alone would
     * leave a failed install with an empty log.
     */
    private static function error(IOInterface $io, string $message): void
    {
        $io->writeError('<error>phaenon/phn: ' . str_replace('<', '\\<', $message) . '</error>', true, IOInterface::QUIET);
    }
}
