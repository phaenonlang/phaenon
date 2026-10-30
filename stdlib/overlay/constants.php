<?php

/**
 * Phaenon overlay: constants whose value php-src's stubs leave UNKNOWN but
 * whose literal matters to the checker — a default of PHP_INT_MAX must not
 * be a negative limit.
 */

const PHP_INT_MAX = 9223372036854775807;
const PHP_INT_MIN = -9223372036854775807 - 1;
/** @var 4|8 */
const PHP_INT_SIZE = 8;

// The CLI SAPI's streams: no stub declares them.
/** @var resource */
const STDIN = null;
/** @var resource */
const STDOUT = null;
/** @var resource */
const STDERR = null;
