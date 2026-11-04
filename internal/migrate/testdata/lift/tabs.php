<?php

declare(strict_types=1);

namespace Tabs;

// Phaenon's whitespace is spaces (design/functions.md §5.5): a tab-indented
// file comes out indented with four spaces per tab stop.
final class Shouter
{
	/**
	 * The text,	shouted.
	 */
	public function shout(string $s): string
	{
		return strtoupper(trim(str_repeat($s, 2)));
	}

	public function table(): string
	{
		$cell = "a	b";
		return <<<EOT
			$cell
				indented	text
			EOT;
	}
}
