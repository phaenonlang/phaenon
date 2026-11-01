<?php

namespace Revolt;

/**
 * Phaenon's declaration of the Revolt event loop's static API (revolt/event-loop 1.0).
 */
final class EventLoop
{
    public static function queue(\Closure $closure, mixed ...$args): void {}

    public static function defer(\Closure $closure): string {}

    public static function delay(float $delay, \Closure $closure): string {}

    public static function repeat(float $interval, \Closure $closure): string {}

    /**
     * @param resource|object $stream
     */
    public static function onReadable(mixed $stream, \Closure $closure): string {}

    /**
     * @param resource|object $stream
     */
    public static function onWritable(mixed $stream, \Closure $closure): string {}

    public static function onSignal(int $signal, \Closure $closure): string {}

    public static function enable(string $callbackId): string {}

    public static function disable(string $callbackId): string {}

    public static function cancel(string $callbackId): void {}

    public static function reference(string $callbackId): string {}

    public static function unreference(string $callbackId): string {}

    public static function getSuspension(): EventLoop\Suspension {}

    public static function run(): void {}
}
