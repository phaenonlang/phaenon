<?php

namespace Revolt\EventLoop;

interface Suspension
{
    public function resume(mixed $value = null): void;

    public function suspend(): mixed;

    public function throw(\Throwable $throwable): void;
}
