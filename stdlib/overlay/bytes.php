<?php

/**
 * Phaenon overlay: PHP's binary producers and consumers typed with bytes
 * (design/strings.md §2, §5). A producer returns bytes; a consumer that
 * works on text and binary alike takes string|bytes; one whose result is
 * binary when its input is binary says so with a conditional return.
 */

/**
 * PHP refuses a length below 1 with a ValueError; PHPStan says positive-int.
 *
 * @param positive-int $length
 * @return bytes
 */
function random_bytes(int $length): string {}

/**
 * @param string|bytes $data
 * @return ($binary is true ? bytes : string)
 */
function hash(string $algo, string $data, bool $binary = false, array $options = []): string {}

/**
 * @param string|bytes $data
 * @param string|bytes $key
 * @return ($binary is true ? bytes : string)
 */
function hash_hmac(string $algo, string $data, #[\SensitiveParameter] string $key, bool $binary = false): string {}

/**
 * @param string|bytes $password
 * @param string|bytes $salt
 * @return ($binary is true ? bytes : string)
 */
function hash_pbkdf2(string $algo, #[\SensitiveParameter] string $password, string $salt, int $iterations, int $length = 0, bool $binary = false, array $options = []): string {}

/**
 * @param string|bytes $known_string
 * @param string|bytes $user_string
 */
function hash_equals(#[\SensitiveParameter] string $known_string, #[\SensitiveParameter] string $user_string): bool {}

/**
 * @param string|bytes $string1
 * @param string|bytes $string2
 */
function strcmp(string $string1, string $string2): int {}

/**
 * The signature is binary; PHP writes it into an untyped reference, so a
 * program checks what came back (PHPStan says mixed as well).
 *
 * @param-out mixed $signature
 */
function openssl_sign(string $data, &$signature, #[\SensitiveParameter] $private_key, string|int $algorithm = OPENSSL_ALGO_SHA1, int $padding = 0, int $salt_length = OPENSSL_RSA_PSS_SALTLEN_AUTO): bool {}

/**
 * The signature it checks is binary, as openssl_sign() writes it.
 *
 * @param string|bytes $data
 * @param string|bytes $signature
 * @param OpenSSLAsymmetricKey|OpenSSLCertificate|array|string $public_key
 */
function openssl_verify(string $data, string $signature, $public_key, string|int $algorithm = OPENSSL_ALGO_SHA1, int $padding = 0, int $salt_length = OPENSSL_RSA_PSS_SALTLEN_AUTO): int|false {}

/**
 * @param string|bytes $string1
 * @param string|bytes $string2
 */
function strncmp(string $string1, string $string2, int $length): int {}

/**
 * @param string|bytes $string
 * @return ($binary is true ? bytes : string)
 */
function md5(string $string, bool $binary = false): string {}

/**
 * @param string|bytes $string
 * @return ($binary is true ? bytes : string)
 */
function sha1(string $string, bool $binary = false): string {}

/** @param string|bytes $string */
function crc32(string $string): int {}

/** @return bytes */
function pack(string $format, mixed ...$values): string {}

/** @param string|bytes $string */
function unpack(string $format, string $string, int $offset = 0): array|false {}

/** @return bytes|false */
function hex2bin(string $string): string|false {}

/** @return bytes|false */
function base64_decode(string $string, bool $strict = false): string|false {}

/** @param string|bytes $string */
function bin2hex(string $string): string {}

/** @param string|bytes $string */
function base64_encode(string $string): string {}

/**
 * @param string|bytes $string
 * @return ($string is bytes ? bytes : string)
 */
function substr(string $string, int $offset, ?int $length = null): string {}

/**
 * @param string|bytes $string
 * @return ($string is bytes ? bytes : string)
 */
function strrev(string $string): string {}

/** @param string|bytes $data */
function hash_update(HashContext $context, string $data): true {}

/** @return ($binary is true ? bytes : string) */
function hash_final(HashContext $context, bool $binary = false): string {}

/*
 * Compressed data is binary; what it decodes to is as much text as
 * file_get_contents() is (strings.md §6).
 */

/**
 * @param string|bytes $data
 * @return bytes|false
 */
function gzencode(string $data, int $level = -1, int $encoding = ZLIB_ENCODING_GZIP): string|false {}

/**
 * @param string|bytes $data
 * @return bytes|false
 */
function gzcompress(string $data, int $level = -1, int $encoding = ZLIB_ENCODING_DEFLATE): string|false {}

/**
 * @param string|bytes $data
 * @return bytes|false
 */
function gzdeflate(string $data, int $level = -1, int $encoding = ZLIB_ENCODING_RAW): string|false {}

/**
 * @param string|bytes $data
 * @return bytes|false
 */
function zlib_encode(string $data, int $encoding, int $level = -1): string|false {}

/**
 * @param string|bytes $data
 * @return bytes|false
 */
function deflate_add(DeflateContext $context, string $data, int $flush_mode = ZLIB_SYNC_FLUSH): string|false {}

/** @param string|bytes $data */
function gzdecode(string $data, int $max_length = 0): string|false {}

/** @param string|bytes $data */
function gzuncompress(string $data, int $max_length = 0): string|false {}

/** @param string|bytes $data */
function gzinflate(string $data, int $max_length = 0): string|false {}

/** @param string|bytes $data */
function zlib_decode(string $data, int $max_length = 0): string|false {}

/** @param string|bytes $data */
function inflate_add(InflateContext $context, string $data, int $flush_mode = ZLIB_SYNC_FLUSH): string|false {}

/** @param string|bytes $character */
function ord(string $character): int {}

/** @param array|string|bytes|null $value */
function mb_check_encoding(array|string|null $value = null, ?string $encoding = null): bool {}

/** @param string|bytes $string */
function mb_scrub(string $string, ?string $encoding = null): string {}

/**
 * @param resource $stream
 * @param string|bytes $data
 */
function fwrite($stream, string $data, ?int $length = null): int|false {}

/**
 * @param resource $stream
 * @param string|bytes $data
 */
function fputs($stream, string $data, ?int $length = null): int|false {}

/**
 * @param bool $strong_result
 * @return bytes
 */
function openssl_random_pseudo_bytes(int $length, &$strong_result = null): string {}

/** @return bytes */
function sodium_crypto_secretbox_keygen(): string {}

/**
 * @param string|bytes $message
 * @param bytes $nonce
 * @param bytes $key
 * @return bytes
 */
function sodium_crypto_secretbox(#[\SensitiveParameter] string $message, string $nonce, #[\SensitiveParameter] string $key): string {}

/**
 * @param bytes $ciphertext
 * @param bytes $nonce
 * @param bytes $key
 * @return bytes|false
 */
function sodium_crypto_secretbox_open(string $ciphertext, string $nonce, #[\SensitiveParameter] string $key): string|false {}

/**
 * @param string|bytes $message
 * @param string|bytes $key
 * @return bytes
 */
function sodium_crypto_generichash(string $message, #[\SensitiveParameter] string $key = "", int $length = SODIUM_CRYPTO_GENERICHASH_BYTES): string {}

/** @return bytes */
function sodium_crypto_sign_keypair(): string {}

/**
 * @param bytes $key_pair
 * @return bytes
 */
function sodium_crypto_sign_secretkey(#[\SensitiveParameter] string $key_pair): string {}

/**
 * @param bytes $key_pair
 * @return bytes
 */
function sodium_crypto_sign_publickey(string $key_pair): string {}

/**
 * @param string|bytes $message
 * @param bytes $secret_key
 * @return bytes
 */
function sodium_crypto_sign_detached(string $message, #[\SensitiveParameter] string $secret_key): string {}

/**
 * @param bytes $signature
 * @param string|bytes $message
 * @param bytes $public_key
 */
function sodium_crypto_sign_verify_detached(string $signature, string $message, string $public_key): bool {}

/** @param bytes $string */
function sodium_bin2hex(#[\SensitiveParameter] string $string): string {}

/** @return bytes */
function sodium_hex2bin(#[\SensitiveParameter] string $string, string $ignore = ""): string {}

/** @param string|bytes $string */
function sodium_memzero(#[\SensitiveParameter] string &$string): void {}
