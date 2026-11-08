<?php

namespace Sentry;

use Sentry\Integration\IntegrationInterface;
use Sentry\State\HubInterface;

/**
 * Phaenon's declaration of the Sentry SDK 4 API that phaenon/debug's
 * Sentry integration (runtime/Debug/Sentry) is written against: the event,
 * its exceptions, stack traces and frames, and the way from the current hub
 * to the client's options. Signatures follow sentry/sentry 4.x; the real
 * package is what runs. Only the programs that check and compile the stdlib
 * read this file (stdlib.Suggested).
 */
final class Event
{
    /**
     * @return ExceptionDataBag[]
     */
    public function getExceptions(): array {}

    public function getStacktrace(): ?Stacktrace {}

    public function setStacktrace(?Stacktrace $stacktrace): self {}
}

final class ExceptionDataBag
{
    public function getStacktrace(): ?Stacktrace {}

    public function setStacktrace(Stacktrace $stacktrace): self {}
}

final class Stacktrace
{
    /**
     * @param Frame[] $frames a non-empty list, ordered from caller to callee
     */
    public function __construct(array $frames) {}

    /**
     * @return Frame[]
     */
    public function getFrames(): array {}
}

final class Frame
{
    public const INTERNAL_FRAME_FILENAME = '[internal]';

    /**
     * @param array<string, mixed> $vars
     */
    public function __construct(?string $functionName, string $file, int $line, ?string $rawFunctionName = null, ?string $absoluteFilePath = null, array $vars = [], bool $inApp = true) {}

    public function getFunctionName(): ?string {}

    public function getRawFunctionName(): ?string {}

    public function getFile(): string {}

    public function getAbsoluteFilePath(): ?string {}

    public function getLine(): int {}

    /**
     * @return string[]
     */
    public function getPreContext(): array {}

    /**
     * @param string[] $preContext
     */
    public function setPreContext(array $preContext): self {}

    public function getContextLine(): ?string {}

    public function setContextLine(?string $contextLine): self {}

    /**
     * @return string[]
     */
    public function getPostContext(): array {}

    /**
     * @param string[] $postContext
     */
    public function setPostContext(array $postContext): self {}

    public function isInApp(): bool {}

    /**
     * @return array<string, mixed>
     */
    public function getVars(): array {}

    public function isInternal(): bool {}
}

final class Options
{
    /**
     * @return string[]
     */
    public function getPrefixes(): array {}
}

interface ClientInterface
{
    public function getOptions(): Options;

    /**
     * @template T of IntegrationInterface
     * @param class-string<T> $className
     * @return T|null
     */
    public function getIntegration(string $className): ?IntegrationInterface;
}

final class SentrySdk
{
    public static function getCurrentHub(): HubInterface {}
}
