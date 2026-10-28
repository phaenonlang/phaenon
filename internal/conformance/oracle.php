<?php
// Lexer oracle: reads file paths from stdin, one per line, and prints one JSON
// line per file with PHP's own tokenization. Run with `php -n` so php.ini
// cannot change short_open_tag (php-src's compiled default is On, which is
// what the .phpt corpus assumes).

declare(strict_types=1);

while (($line = fgets(STDIN)) !== false) {
    $path = rtrim($line, "\r\n");
    if ($path === '') {
        continue;
    }
    $src = file_get_contents($path);
    $out = [];
    foreach (PhpToken::tokenize($src) as $t) {
        $out[] = [$t->getTokenName(), base64_encode($t->text)]; // base64: token text need not be UTF-8
    }
    echo json_encode(['file' => $path, 'tokens' => $out]), "\n";
}
