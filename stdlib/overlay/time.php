<?php

/**
 * Phaenon overlay: time functions whose return type follows an argument.
 */

/**
 * @return ($as_float is true ? float : string)
 */
function microtime(bool $as_float = false): string|float {}
