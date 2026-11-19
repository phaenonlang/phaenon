<?php

declare(strict_types=1);

// Runs the compiled program: each closure a partial application was
// lowered to is called without the parameters `...` left, so that the
// callee's defaults are what it returns, as PHP 8.6 returns them.

require __DIR__ . '/Globals.php';
require __DIR__ . '/Other/Other.php';
require __DIR__ . '/Shop/Shop.php';
require __DIR__ . '/Imp/Imp.php';
require __DIR__ . '/App/App.php';

echo json_encode(\App\Defaults::all(), JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES), "\n";
$corner = new \Shop\Corner();
echo json_encode(\App\Defaults::late($corner, $corner, $corner, new \Shop\Right()), JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES), "\n";
