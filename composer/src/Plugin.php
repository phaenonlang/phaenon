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
 * design/tooling.md §3.7, T18): those are left as installed. A package whose installed roots are
 * all committed is nothing to build, and one that came without the outputs it commits fails the
 * dump before any binary is fetched, as a failed build does. Only a package says so, about itself:
 * the root project is built whatever it commits.
 *
 * A project with nothing to build, its own or installed, needs no binary, and the plugin does
 * nothing. An `install --no-autoloader` builds at the `dump-autoload` that follows. Under
 * `--no-plugins` nothing here runs, and vendor/bin/phn fetches the binary when it is first used.
 */
final class Plugin implements PluginInterface, EventSubscriberInterface
{
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
        $incomplete = 0;
        foreach (self::phaenonPackages($composer) as $name => [$version, $dir]) {
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
            foreach ($file->builtBefore($dir) as $output) {
                self::warnOfAnEarlierBuild($io, trim($name . ' ' . $version), $dir, $output, $projectDir);
            }
            if ($errors !== []) {
                ++$incomplete;
            } elseif (!$file->leavesNothingToBuild($dir)) {
                $packages[] = $name;
            }
        }
        if ($incomplete > 0) {
            throw self::failure($io, ProjectFile::summary($incomplete), 1);
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
     * @return array<string, array{string, string}> the version of each as Composer shows it and
     *                                              where it is installed, by its name
     */
    private static function phaenonPackages(Composer $composer): array
    {
        $installationManager = $composer->getInstallationManager();
        $packages = [];
        foreach ($composer->getRepositoryManager()->getLocalRepository()->getCanonicalPackages() as $package) {
            $path = $installationManager->getInstallPath($package);
            if ($path !== null && is_file($path . '/phaenon.json')) {
                $packages[$package->getPrettyName()] = [$package->getPrettyVersion(), $path];
            }
        }
        ksort($packages, \SORT_STRING);

        return $packages;
    }

    /**
     * A committed root that holds the stamp of a build was built here by an earlier release of
     * the package, one that shipped its sources alone, and an upgrade that kept the directory (a
     * source install, which is a git checkout) left that build's outputs in it: a class the new
     * release dropped still loads. The root is left as installed all the same, and the project
     * is told how to clear it.
     */
    private static function warnOfAnEarlierBuild(IOInterface $io, string $package, string $dir, string $output, string $projectDir): void
    {
        $shown = str_starts_with($dir, $projectDir . '/') ? substr($dir, \strlen($projectDir) + 1) : $dir;
        $message = sprintf(
            '%s: %s/%s is the stamp of an earlier build of this package, in a root it now commits. '
            . 'Outputs of that build were left there, and those the release no longer has still load. %s',
            $package,
            $output,
            ProjectFile::STAMP,
            is_link($dir)
                ? sprintf('The package is a link to a checkout: run `phn build` in %s, which removes them, and delete the stamp', $shown)
                : sprintf('Reinstalling the package clears them: remove %s and run `composer install`', $shown),
        );
        $io->writeError('<warning>phaenon/phn: ' . str_replace('<', '\\<', $message) . '</warning>');
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
