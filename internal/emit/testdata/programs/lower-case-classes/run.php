<?php

declare(strict_types=1);

// Runs the compiled program: PHP checks a parameter's type when the method
// is called, so the classes named Map and Bytes are passed where the
// signatures name them, and arrays and strings where they say map and
// bytes.

require __DIR__ . '/Lib/Map.php';
require __DIR__ . '/Lib/Bytes.php';
require __DIR__ . '/Kinds/Map.php';
require __DIR__ . '/Use/Store.php';
require __DIR__ . '/Use/Routes.php';

$store = new \Words\Use\Store();
$blobs = $store->put('a', "\x00\x01");
echo get_class($blobs), "\n";
$a = $blobs->get('a');
echo $a === null ? 'none' : strlen($store->raw($a)), "\n";
echo $store->count(['x' => 1, 'y' => 2]), "\n";
/** @var \Words\Lib\Map<int, \Words\Lib\Bytes> $first */
$first = new \Words\Lib\Map([0 => new \Words\Lib\Bytes('abc')]);
echo $store->first($first)?->raw, "\n";
echo count($store->all(new \Words\Lib\Bytes('a'), new \Words\Lib\Bytes('b'))), "\n";

$routes = new \Words\Use\Routes();
echo $routes->name(\Words\Kinds\Map::Sea), ' ', $routes->size(['size' => 3]), "\n";
