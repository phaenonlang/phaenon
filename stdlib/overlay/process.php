<?php

/**
 * Phaenon overlay: the process functions whose by-reference outputs the
 * php-src stubs leave untyped.
 */

/**
 * @param-out list<string> $output
 * @param-out int $result_code
 */
function exec(string $command, &$output = null, &$result_code = null): string|false {}
