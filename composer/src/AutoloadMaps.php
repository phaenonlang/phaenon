<?php

declare(strict_types=1);

namespace Phaenon\Composer;

use Composer\Autoload\AutoloadGenerator;
use Composer\Composer;
use Composer\Filter\PlatformRequirementFilter\PlatformRequirementFilterFactory;
use Composer\IO\NullIO;
use Composer\Package\PackageInterface;

/**
 * Writes vendor/composer's autoload maps for the packages installed now, before phn builds.
 *
 * phn reads vendor code through those maps (autoload_psr4.php, autoload_classmap.php,
 * autoload_namespaces.php, autoload_files.php). When pre-autoload-dump fires they do not exist yet
 * on a fresh install and are stale after an update, while installed.json is already current. This
 * dump brings them up to date; Composer's own dump runs after the builds and overwrites them,
 * scanning the outputs phn wrote when it is optimized or authoritative.
 */
final class AutoloadMaps extends AutoloadGenerator
{
    /**
     * @param bool $devMode the dump's, so that the maps hold the packages Composer's will
     */
    public static function write(Composer $composer, bool $devMode): void
    {
        // A generator of its own leaves Composer's (authoritative, APCu, its scripts flag) as
        // Composer set it. Running no scripts, it dispatches no autoload-dump event back to us.
        $generator = new self($composer->getEventDispatcher(), new NullIO());
        $generator->setDevMode($devMode);
        $generator->setRunScripts(false);
        // phn reads the maps and never runs the autoloader: the platform check is left to
        // Composer's dump, which knows the --ignore-platform-req options.
        $generator->setPlatformRequirementFilter(PlatformRequirementFilterFactory::ignoreAll());
        $generator->dump(
            $composer->getConfig(),
            $composer->getRepositoryManager()->getLocalRepository(),
            $composer->getPackage(),
            $composer->getInstallationManager(),
            'composer',
            false,
            // No suffix of our own: Composer derives it from the lock (2.4 and later take the
            // locker, 2.3 ignores the argument), and its dump carries it over from autoload.php.
            '',
            $composer->getLocker(),
        );
    }

    /**
     * A classmap entry may name an output phn has yet to write, and a dump fails on a path it
     * cannot scan: those are left out here, and Composer's dump finds them written.
     *
     * @param bool|string[] $filteredDevPackages
     *
     * @return array{
     *     'psr-0': array<string, array<string>>,
     *     'psr-4': array<string, array<string>>,
     *     'classmap': array<int, string>,
     *     'files': array<string, string>,
     *     'exclude-from-classmap': array<int, string>,
     * }
     */
    #[\Override]
    public function parseAutoloads(array $packageMap, PackageInterface $rootPackage, $filteredDevPackages = false): array
    {
        $autoloads = parent::parseAutoloads($packageMap, $rootPackage, $filteredDevPackages);
        $autoloads['classmap'] = array_values(array_filter(
            $autoloads['classmap'],
            static fn (string $path): bool => file_exists($path) || (str_contains($path, '*') && (glob($path) ?: []) !== []),
        ));

        return $autoloads;
    }
}
