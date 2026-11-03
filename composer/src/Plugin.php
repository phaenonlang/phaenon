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
 * A project with no Phaenon code, its own or installed, needs no binary, and the plugin does
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
        $packages = self::phaenonPackages($composer);
        $buildsProject = $settings->buildRoot && is_file($projectDir . '/phaenon.json');
        if ($packages === [] && !$buildsProject) {
            $io->writeError(
                'phaenon/phn: nothing to build: no installed package has a phaenon.json, and the project builds none',
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
     * The installed packages that have a phaenon.json, the ones `phn build --vendor` builds.
     *
     * @return list<string> their names
     */
    private static function phaenonPackages(Composer $composer): array
    {
        $installationManager = $composer->getInstallationManager();
        $names = [];
        foreach ($composer->getRepositoryManager()->getLocalRepository()->getCanonicalPackages() as $package) {
            $path = $installationManager->getInstallPath($package);
            if ($path !== null && is_file($path . '/phaenon.json')) {
                $names[] = $package->getPrettyName();
            }
        }

        return $names;
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
    private static function failure(IOInterface $io, string $message, int $status, \Throwable $previous): ScriptExecutionException
    {
        $io->writeError('<error>phaenon/phn: ' . str_replace('<', '\\<', $message) . '</error>');

        return new ScriptExecutionException($message, $status, $previous);
    }
}
