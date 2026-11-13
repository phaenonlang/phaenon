<?php

declare(strict_types=1);

namespace Phaenon\Composer;

use Composer\IO\IOInterface;
use Composer\Util\Platform;
use Composer\Util\ProcessExecutor;

/**
 * Runs phn in the root project's directory, its output passed on to Composer's as phn writes it.
 */
final readonly class Build
{
    public function __construct(
        private string $binary,
        private string $projectDir,
        private ProcessExecutor $executor,
        private IOInterface $io,
    ) {
    }

    /**
     * @param list<string>                    $arguments
     * @param string                          $meaning     what a failure means, for the message
     * @param array<non-empty-string, string> $environment variables phn is run with, and that are
     *                                                     unset again when it has run
     *
     * @throws BuildFailed when phn exits with a non-zero status, which is its code
     */
    public function run(array $arguments, string $meaning, array $environment = []): void
    {
        $command = implode(' ', ['phn', ...$arguments]);
        // Composer announces a script it runs the same way.
        $this->io->writeError('> ' . $command);
        $io = $this->io;
        // phn's streams go where Composer sends a script's: its stdout to Composer's stdout, its
        // diagnostics to Composer's stderr. Symfony Process calls stderr 'err'.
        $forward = static function (string $type, string $buffer) use ($io): void {
            if ($type === 'err') {
                $io->writeErrorRaw($buffer, false);
            } else {
                $io->writeRaw($buffer, false);
            }
        };
        // The process inherits Composer's environment.
        foreach ($environment as $name => $value) {
            Platform::putEnv($name, $value);
        }
        try {
            $status = $this->executor->execute([$this->binary, ...$arguments], $forward, $this->projectDir);
        } finally {
            foreach (array_keys($environment) as $name) {
                Platform::clearEnv($name);
            }
        }
        if ($status !== 0) {
            throw new BuildFailed(
                sprintf('`%s` exited with status %d in %s: %s', $command, $status, $this->projectDir, $meaning),
                $status,
            );
        }
    }
}
