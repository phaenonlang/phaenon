<?php

declare(strict_types=1);

namespace Phaenon\Composer;

/**
 * The credentials Composer would send to a host, as the launcher reads them without Composer: the
 * `http-basic` and `bearer` entries of auth.json in COMPOSER_HOME, of auth.json beside the root
 * composer.json and of COMPOSER_AUTH, each overriding the one before as in Composer. A mirror in
 * a private registry needs them.
 */
final readonly class Credentials
{
    /**
     * @param array<string, string> $authorization the Authorization header by origin: the host,
     *                                             with its port when the URL names one
     */
    private function __construct(
        private array $authorization,
    ) {
    }

    public static function find(string $projectDir): self
    {
        $home = self::composerHome();
        $sources = [
            $home === null ? false : @file_get_contents($home . '/auth.json'),
            @file_get_contents($projectDir . '/auth.json'),
            getenv('COMPOSER_AUTH'),
        ];
        $authorization = [];
        foreach ($sources as $json) {
            $auth = \is_string($json) && $json !== '' ? json_decode($json, true) : null;
            if (!\is_array($auth)) {
                continue;
            }
            $basic = $auth['http-basic'] ?? [];
            foreach (\is_array($basic) ? $basic : [] as $host => $entry) {
                $username = \is_array($entry) ? ($entry['username'] ?? null) : null;
                $password = \is_array($entry) ? ($entry['password'] ?? null) : null;
                if (\is_string($username) && \is_string($password)) {
                    $authorization[(string) $host] = 'Basic ' . base64_encode($username . ':' . $password);
                }
            }
            $bearer = $auth['bearer'] ?? [];
            foreach (\is_array($bearer) ? $bearer : [] as $host => $token) {
                if (\is_string($token)) {
                    $authorization[(string) $host] = 'Bearer ' . $token;
                }
            }
        }

        return new self($authorization);
    }

    /**
     * The Authorization header for the origin, if there is one.
     */
    public function for(string $origin): ?string
    {
        return $this->authorization[$origin] ?? null;
    }

    /**
     * Composer's home directory as Composer finds it when COMPOSER_HOME is not set.
     */
    private static function composerHome(): ?string
    {
        $home = getenv('COMPOSER_HOME');
        if (\is_string($home) && $home !== '') {
            return $home;
        }
        if (PHP_OS_FAMILY === 'Windows') {
            $appData = getenv('APPDATA');

            return \is_string($appData) && $appData !== '' ? $appData . '/Composer' : null;
        }
        $user = getenv('HOME');
        if (!\is_string($user) || $user === '') {
            return null;
        }
        if (is_dir($user . '/.composer')) {
            return $user . '/.composer';
        }
        $config = getenv('XDG_CONFIG_HOME');

        return (\is_string($config) && $config !== '' ? $config : $user . '/.config') . '/composer';
    }
}
