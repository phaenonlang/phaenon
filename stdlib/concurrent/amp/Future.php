<?php

namespace Amp\Future;

use Amp\Cancellation;
use Amp\Future;

/**
 * Phaenon's declaration of amp's Future combinators (amphp/amp 3.1).
 */

/**
 * @template T
 * @param iterable<Future<T>> $futures
 * @return T
 */
function awaitFirst(iterable $futures, ?Cancellation $cancellation = null): mixed {}

/**
 * @template T
 * @param iterable<Future<T>> $futures
 * @return T
 */
function awaitAny(iterable $futures, ?Cancellation $cancellation = null): mixed {}

/**
 * @template Tk of array-key
 * @template Tv
 * @param iterable<Tk, Future<Tv>> $futures
 * @return array<Tk, Tv>
 */
function awaitAnyN(int $count, iterable $futures, ?Cancellation $cancellation = null): array {}

/**
 * @template Tk of array-key
 * @template Tv
 * @param iterable<Tk, Future<Tv>> $futures
 * @return array{array<Tk, \Throwable>, array<Tk, Tv>}
 */
function awaitAll(iterable $futures, ?Cancellation $cancellation = null): array {}

/**
 * @template Tk of array-key
 * @template Tv
 * @param iterable<Tk, Future<Tv>> $futures
 * @return array<Tk, Tv>
 */
function await(iterable $futures, ?Cancellation $cancellation = null): array {}
