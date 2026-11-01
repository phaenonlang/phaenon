<?php

namespace Amp;

/**
 * Phaenon's declaration of the amp v3 API that compiled concurrency code
 * relies on (design/concurrency.md §8): the handle, the cancellation tokens
 * and the two functions. Signatures follow amphp/amp 3.1; the real package
 * is what runs.
 */
interface Cancellation
{
    /**
     * @param \Closure(CancelledException): void $callback
     */
    public function subscribe(\Closure $callback): string;

    public function unsubscribe(string $id): void;

    public function isRequested(): bool;

    /**
     * @throws CancelledException
     */
    public function throwIfRequested(): void;
}

class CancelledException extends \Exception
{
    public function __construct(?\Throwable $previous = null) {}
}

class TimeoutException extends \Exception
{
    public function __construct(string $message = 'Operation timed out') {}
}

/**
 * @template-covariant T
 */
final class Future
{
    /**
     * @template Tk of array-key
     * @template Tv
     * @param iterable<Tk, Future<Tv>> $futures
     * @return iterable<Tk, Future<Tv>>
     */
    public static function iterate(iterable $futures, ?Cancellation $cancellation = null): iterable {}

    /**
     * @template Tv
     * @param Tv $value
     * @return Future<Tv>
     */
    public static function complete(mixed $value = null): Future {}

    /**
     * @return Future<never>
     */
    public static function error(\Throwable $throwable): Future {}

    public function isComplete(): bool {}

    /**
     * @return Future<T>
     */
    public function ignore(): Future {}

    /**
     * @template Tr
     * @param \Closure(T): Tr $map
     * @return Future<Tr>
     */
    public function map(\Closure $map): Future {}

    /**
     * @template Tr
     * @param \Closure(\Throwable): Tr $catch
     * @return Future<Tr>
     */
    public function catch(\Closure $catch): Future {}

    /**
     * @param \Closure(): void $finally
     * @return Future<T>
     */
    public function finally(\Closure $finally): Future {}

    /**
     * @return T
     */
    public function await(?Cancellation $cancellation = null): mixed {}
}

/**
 * @template T
 */
final class DeferredFuture
{
    public function isComplete(): bool {}

    /**
     * @param T $value
     */
    public function complete(mixed $value = null): void {}

    public function error(\Throwable $throwable): void {}

    /**
     * @return Future<T>
     */
    public function getFuture(): Future {}
}

final class DeferredCancellation
{
    public function getCancellation(): Cancellation {}

    public function isCancelled(): bool {}

    public function cancel(?\Throwable $previous = null): void {}
}

final class TimeoutCancellation implements Cancellation
{
    public function __construct(float $timeout, string $message = 'Operation timed out') {}

    public function subscribe(\Closure $callback): string {}

    public function unsubscribe(string $id): void {}

    public function isRequested(): bool {}

    public function throwIfRequested(): void {}
}

final class CompositeCancellation implements Cancellation
{
    public function __construct(Cancellation ...$cancellations) {}

    public function subscribe(\Closure $callback): string {}

    public function unsubscribe(string $id): void {}

    public function isRequested(): bool {}

    public function throwIfRequested(): void {}
}

final class NullCancellation implements Cancellation
{
    public function subscribe(\Closure $callback): string {}

    public function unsubscribe(string $id): void {}

    public function isRequested(): bool {}

    public function throwIfRequested(): void {}
}

final class SignalCancellation implements Cancellation
{
    /**
     * @param int|list<int> $signals
     */
    public function __construct(int|array $signals, string $message = 'Operation cancelled by signal') {}

    public function subscribe(\Closure $callback): string {}

    public function unsubscribe(string $id): void {}

    public function isRequested(): bool {}

    public function throwIfRequested(): void {}
}

/**
 * @template T
 * @param \Closure(mixed ...): T $closure
 * @return Future<T>
 */
function async(\Closure $closure, mixed ...$args): Future {}

function delay(float $timeout, bool $reference = true, ?Cancellation $cancellation = null): void {}

function now(): float {}
