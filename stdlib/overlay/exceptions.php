<?php

/**
 * Phaenon overlay: what PHPStan's own stubs say of exceptions. The getters
 * of Throwable throw nothing, so a call to one in a try block reaches no
 * catch (design/tooling.md §9.1). The methods listed replace the stub's,
 * everything else stays as the stub declares it.
 */

interface Throwable extends Stringable
{
    /** @throws void */
    public function getMessage(): string;

    /**
     * @return int
     * @throws void
     */
    public function getCode();

    /** @throws void */
    public function getFile(): string;

    /** @throws void */
    public function getLine(): int;

    /** @throws void */
    public function getTrace(): array;

    /** @throws void */
    public function getPrevious(): ?Throwable;

    /** @throws void */
    public function getTraceAsString(): string;
}

class Exception implements Throwable
{
    /** @throws void */
    final public function getMessage(): string {}

    /**
     * @return int
     * @throws void
     */
    final public function getCode() {}

    /** @throws void */
    final public function getFile(): string {}

    /** @throws void */
    final public function getLine(): int {}

    /** @throws void */
    final public function getTrace(): array {}

    /** @throws void */
    final public function getPrevious(): ?Throwable {}

    /** @throws void */
    final public function getTraceAsString(): string {}
}

class Error implements Throwable
{
    /** @throws void */
    final public function getMessage(): string {}

    /**
     * @return int
     * @throws void
     */
    final public function getCode() {}

    /** @throws void */
    final public function getFile(): string {}

    /** @throws void */
    final public function getLine(): int {}

    /** @throws void */
    final public function getTrace(): array {}

    /** @throws void */
    final public function getPrevious(): ?Throwable {}

    /** @throws void */
    final public function getTraceAsString(): string {}
}
