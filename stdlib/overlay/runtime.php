<?php

/**
 * Phaenon overlay: the process and its environment.
 */

/**
 * @return ($name is null ? array<string, string> : string|false)
 */
function getenv(?string $name = null, bool $local_only = false): string|array|false {}

/**
 * @param-out list<resource> $pipes
 * @return resource|false
 */
function proc_open(array|string $command, array $descriptor_spec, &$pipes, ?string $cwd = null, ?array $env_vars = null, ?array $options = null) {}
