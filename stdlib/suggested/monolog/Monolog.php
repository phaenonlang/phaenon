<?php

namespace Monolog;

/**
 * Phaenon's declaration of the Monolog 3 API that phaenon/debug's Monolog
 * processor (runtime/Debug/Monolog) is written against. Signatures follow
 * monolog/monolog 3.x; the real package is what runs. Only the programs that
 * check and compile the stdlib read this file (stdlib.Suggested).
 */
class LogRecord
{
    /**
     * @param array<mixed> $context
     * @param array<mixed> $extra
     */
    public function __construct(
        public readonly \DateTimeImmutable $datetime,
        public readonly string $channel,
        public readonly Level $level,
        public readonly string $message,
        public readonly array $context = [],
        public array $extra = [],
        public mixed $formatted = null,
    ) {}

    public function with(mixed ...$args): self {}
}

enum Level: int
{
    case Debug = 100;
    case Info = 200;
    case Notice = 250;
    case Warning = 300;
    case Error = 400;
    case Critical = 500;
    case Alert = 550;
    case Emergency = 600;
}
