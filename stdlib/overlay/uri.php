<?php

/**
 * Phaenon overlay: what Uri\WhatWg's methods write into the list of errors
 * they take by reference, as PHPStan types it once the call returns (see
 * writes.php).
 */

namespace Uri\WhatWg;

final class UrlBuilder
{
    /**
     * @param array $softErrors
     * @param-out mixed $softErrors
     */
    public function build(?\Uri\WhatWg\Url $baseUrl = null, &$softErrors = null): \Uri\WhatWg\Url {}
}

final readonly class Url
{
    /**
     * @param array $errors
     * @param-out mixed $errors
     */
    public static function parse(string $uri, ?\Uri\WhatWg\Url $baseUrl = null, &$errors = null): ?static {}

    /**
     * @param array $softErrors
     * @param-out mixed $softErrors
     */
    public function __construct(string $uri, ?\Uri\WhatWg\Url $baseUrl = null, &$softErrors = null) {}

    /**
     * @param array $softErrors
     * @param-out mixed $softErrors
     */
    public function resolve(string $uri, &$softErrors = null): static {}
}
