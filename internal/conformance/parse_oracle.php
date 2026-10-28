<?php
// Parser oracle: for each path on stdin prints "<path>\tok" or "<path>\terror\t<message>"
// according to PHP's own parser (PhpToken::tokenize with TOKEN_PARSE runs zendparse).

declare(strict_types=1);

while (($line = fgets(STDIN)) !== false) {
    $path = rtrim($line, "\r\n");
    if ($path === '') {
        continue;
    }
    $src = file_get_contents($path);
    try {
        PhpToken::tokenize($src, TOKEN_PARSE);
        echo $path, "\tok\n";
    } catch (ParseError $e) {
        echo $path, "\terror\t", str_replace(["\n", "\t"], ' ', $e->getMessage()), "\n";
    } catch (Throwable $e) {
        echo $path, "\tother\t", get_class($e), ': ', str_replace(["\n", "\t"], ' ', $e->getMessage()), "\n";
    }
}
