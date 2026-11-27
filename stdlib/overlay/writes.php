<?php

/**
 * Phaenon overlay: what PHP's own functions write into an argument they take by
 * reference, typed as PHPStan types it once the call returns. PHPStan's
 * signature map marks each such parameter as one that creates its variable;
 * the type is its stubs' @param-out, or its extension's, where it has one,
 * and mixed where it has neither, whatever the map says PHP writes. A
 * by-reference parameter PHPStan reads and writes back unchanged (a sort,
 * end(), settype()) has no tag here: the call keeps the argument's type.
 */

/**
 * @param bool $definitely_prime
 * @param-out mixed $definitely_prime
 */
function gmp_prevprime(GMP|int|string $num, &$definitely_prime = null): GMP {}

/**
 * @param string $response
 * @param-out mixed $response
 */
function ftp_alloc(FTP\Connection $ftp, int $size, &$response = null): bool {}

/**
 * @param-out ($read is not null ? array<Socket> : null) $read
 * @param-out ($write is not null ? array<Socket> : null) $write
 * @param-out ($except is not null ? array<Socket> : null) $except
 */
function socket_select(?array &$read, ?array &$write, ?array &$except, ?int $seconds, int $microseconds = 0): int|false {}

/**
 * @param string $address
 * @param int $port
 * @param-out mixed $address
 * @param-out mixed $port
 */
function socket_getsockname(Socket $socket, &$address, &$port = null): bool {}

/**
 * @param string $address
 * @param int $port
 * @param-out mixed $address
 * @param-out mixed $port
 */
function socket_getpeername(Socket $socket, &$address, &$port = null): bool {}

/**
 * @param string|null $data
 * @param-out mixed $data
 */
function socket_recv(Socket $socket, &$data, int $length, int $flags): int|false {}

/**
 * @param string $data
 * @param string $address
 * @param int $port
 * @param-out mixed $data
 * @param-out mixed $address
 * @param-out mixed $port
 */
function socket_recvfrom(Socket $socket, &$data, int $length, int $flags, &$address, &$port = null): int|false {}

/**
 * @param array $pair
 * @param-out mixed $pair
 */
function socket_create_pair(int $domain, int $type, int $protocol, &$pair): bool {}

/** @param-out mixed $message */
function socket_recvmsg(Socket $socket, array &$message, int $flags = 0): int|false {}

/**
 * @return array<int, AddressInfo>|false
 * @param int $error_code
 * @param-out mixed $error_code
 */
function socket_addrinfo_lookup(string $host, ?string $service = null, array $hints = [], &$error_code = null): array|false {}

/**
 * @param string|int|float|bool $message
 * @param int $error_code
 * @param-out mixed $error_code
 */
function msg_send(SysvMessageQueue $queue, int $message_type, $message, bool $serialize = true, bool $blocking = true, &$error_code = null): bool {}

/**
 * @param int $received_message_type
 * @param int $error_code
 * @param-out mixed $received_message_type
 * @param-out mixed $message
 * @param-out mixed $error_code
 */
function msg_receive(
    SysvMessageQueue $queue,
    int $desired_message_type,
    &$received_message_type,
    int $max_message_size,
    mixed &$message,
    bool $unserialize = true,
    int $flags = 0,
    &$error_code = null
): bool {}

/**
 * @param string $currency
 * @param int $offset
 * @param-out mixed $currency
 */
function numfmt_parse_currency(NumberFormatter $formatter, string $string, &$currency, &$offset = null): float|false {}

/**
 * @param int $next
 * @param-out mixed $next
 */
function grapheme_extract(string $haystack, int $size, int $type = GRAPHEME_EXTR_COUNT, int $offset = 0, &$next = null): string|false {}

/**
 * @param array $idna_info
 * @param-out mixed $idna_info
 */
function idn_to_ascii(string $domain, int $flags = IDNA_DEFAULT, int $variant = INTL_IDNA_VARIANT_UTS46, &$idna_info = null): string|false {}

/**
 * @param array $idna_info
 * @param-out mixed $idna_info
 */
function idn_to_utf8(string $domain, int $flags = IDNA_DEFAULT, int $variant = INTL_IDNA_VARIANT_UTS46, &$idna_info = null): string|false {}

/**
 * @param bool $isSystemId
 * @param-out mixed $isSystemId
 */
function intltz_get_canonical_id(string $timezoneId, &$isSystemId = null): string|false {}

/**
 * @param int $rawOffset
 * @param int $dstOffset
 * @param-out mixed $rawOffset
 * @param-out mixed $dstOffset
 */
function intltz_get_offset(IntlTimeZone $timezone, float $timestamp, bool $local, &$rawOffset, &$dstOffset): bool {}

/**
 * @param array|string|int $value
 * @param-out mixed $value
 */
function ldap_get_option(?LDAP\Connection $ldap, int $option, &$value = null): bool {}

/**
 * @param int $error_code
 * @param string $matched_dn
 * @param string $error_message
 * @param array $referrals
 * @param array $controls
 * @param-out mixed $error_code
 * @param-out mixed $matched_dn
 * @param-out mixed $error_message
 * @param-out mixed $referrals
 * @param-out mixed $controls
 */
function ldap_parse_result(LDAP\Connection $ldap, LDAP\Result $result, &$error_code, &$matched_dn = null, &$error_message = null, &$referrals = null, &$controls = null): bool {}

/**
 * @param string $response_data
 * @param string $response_oid
 * @param-out mixed $response_data
 * @param-out mixed $response_oid
 */
function ldap_exop(LDAP\Connection $ldap, string $request_oid, ?string $request_data = null, ?array $controls = null, &$response_data = UNKNOWN, &$response_oid = null): LDAP\Result|bool {}

/**
 * @param string $response_data
 * @param string $response_oid
 * @param-out mixed $response_data
 * @param-out mixed $response_oid
 */
function ldap_exop_sync(LDAP\Connection $ldap, string $request_oid, ?string $request_data = null, ?array $controls = null, &$response_data = null, &$response_oid = null): LDAP\Result|bool {}

/**
 * @param string $response_data
 * @param string $response_oid
 * @param-out mixed $response_data
 * @param-out mixed $response_oid
 */
function ldap_parse_exop(LDAP\Connection $ldap, LDAP\Result $result, &$response_data = null, &$response_oid = null): bool {}

/**
 * @param array $values
 * @param array $index
 * @param-out mixed $values
 * @param-out mixed $index
 */
function xml_parse_into_struct(XMLParser $parser, string $data, &$values, &$index = null): int|false {}

/**
 * @param int $rest_index
 * @return array<int|string, string|array|false>|false
 * @param-out positive-int $rest_index
 */
function getopt(string $short_options, array $long_options = [], &$rest_index = null): array|false {}

/**
 * @param array $authoritative_name_servers
 * @param array $additional_records
 * @return array<int, array>|false
 * @param-out mixed $authoritative_name_servers
 * @param-out mixed $additional_records
 */
function dns_get_record(string $hostname, int $type = DNS_ANY, &$authoritative_name_servers = null, &$additional_records = null, bool $raw = false): array|false {}

/**
 * @param array $hosts
 * @param array $weights
 * @param-out mixed $hosts
 * @param-out mixed $weights
 */
function dns_get_mx(string $hostname, &$hosts, &$weights = null): bool {}

/**
 * @param array $hosts
 * @param array $weights
 * @param-out list<string> $hosts
 * @param-out list<int> $weights
 */
function getmxrr(string $hostname, &$hosts, &$weights = null): bool {}

/**
 * @param string $filename
 * @param int $line
 * @param-out string $filename
 * @param-out int $line
 */
function headers_sent(&$filename = null, &$line = null): bool {}

/**
 * @param float $percent
 * @param-out float $percent
 */
function similar_text(string $string1, string $string2, &$percent = null): int {}

/**
 * PHPStan's extension keeps the case of a lowercase string in the values
 * (and of an uppercase one, which the checker has no type for).
 *
 * @param array $result
 * @param-out ($string is lowercase-string ? array<int|string, array<mixed>|lowercase-string> : array<int|string, array<mixed>|string>) $result
 */
function parse_str(string $string, &$result): void {}

/**
 * @param int $result_code
 * @param-out int $result_code
 */
function system(string $command, &$result_code = null): string|false {}

/**
 * @param int $result_code
 * @param-out int $result_code
 */
function passthru(string $command, &$result_code = null): false|null {}

/**
 * @param resource $stream
 * @param int $would_block
 * @param-out 0|1 $would_block
 */
function flock($stream, int $operation, &$would_block = null): bool {}

/**
 * @param resource $stream
 * @return array<int, mixed>|int|false|null
 * @param-out string|int|float|null $vars
 */
function fscanf($stream, string $format, mixed &...$vars): array|int|false|null {}

/**
 * @param int $error_code
 * @param string $error_message
 * @return resource|false
 * @param-out int $error_code
 * @param-out string $error_message
 */
function fsockopen(string $hostname, int $port = -1, &$error_code = null, &$error_message = null, ?float $timeout = null) {}

/**
 * @param int $error_code
 * @param string $error_message
 * @return resource|false
 * @param-out mixed $error_code
 * @param-out mixed $error_message
 */
function pfsockopen(string $hostname, int $port = -1, &$error_code = null, &$error_message = null, ?float $timeout = null) {}

/**
 * @param array $image_info
 * @return array<int|string, int|string>|false
 * @param-out mixed $image_info
 */
function getimagesize(string $filename, &$image_info = null): array|false {}

/**
 * @param array $image_info
 * @return array<int|string, int|string>|false
 * @param-out mixed $image_info
 */
function getimagesizefromstring(string $string, &$image_info = null): array|false {}

/**
 * @param int $error_code
 * @param string $error_message
 * @param resource|null $context
 * @return resource|false
 * @param-out int|null $error_code
 * @param-out string|null $error_message
 */
function stream_socket_client(string $address, &$error_code = null, &$error_message = null, ?float $timeout = null, int $flags = STREAM_CLIENT_CONNECT, $context = null) {}

/**
 * @param int $error_code
 * @param string $error_message
 * @param resource|null $context
 * @return resource|false
 * @param-out int|null $error_code
 * @param-out string|null $error_message
 */
function stream_socket_server(string $address, &$error_code = null, &$error_message = null, int $flags = STREAM_SERVER_BIND | STREAM_SERVER_LISTEN, $context = null) {}

/**
 * @param resource $socket
 * @param string $peer_name
 * @return resource|false
 * @param-out mixed $peer_name
 */
function stream_socket_accept($socket, ?float $timeout = null, &$peer_name = null) {}

/**
 * @param resource $socket
 * @param string|null $address
 * @param-out mixed $address
 */
function stream_socket_recvfrom($socket, int $length, int $flags = 0, &$address = null): string|false {}

/**
 * @param string $callable_name
 * @param-out callable-string $callable_name
 */
function is_callable(mixed $value, bool $syntax_only = false, &$callable_name = null): bool {}

/**
 * A list of the values, non-empty where the array was: PHPStan's
 * shuffleArray(), a write of what the array held.
 *
 * @template V
 * @param array<V> $array
 * @param-out ($array is non-empty-array ? non-empty-list<V> : list<V>) $array
 */
function shuffle(array &$array): true {}

/**
 * @param int $still_running
 * @param-out mixed $still_running
 */
function curl_multi_exec(CurlMultiHandle $multi_handle, &$still_running): int {}

/**
 * @param int $queued_messages
 * @return array<string, int|object>|false
 * @param-out mixed $queued_messages
 */
function curl_multi_info_read(CurlMultiHandle $multi_handle, &$queued_messages = null): array|false {}

/**
 * @param array $array
 * @param-out mixed $array
 */
function odbc_fetch_into(Odbc\Result $statement, &$array, ?int $row = null): int|false {}

/** @param-out mixed $vars */
function mysqli_stmt_bind_result(mysqli_stmt $statement, mixed &...$vars): bool {}

/**
 * @param resource|string $file
 * @param int $width
 * @param int $height
 * @param int $image_type
 * @param-out mixed $width
 * @param-out mixed $height
 * @param-out mixed $image_type
 */
function exif_thumbnail($file, &$width = null, &$height = null, &$image_type = null): string|false {}

/**
 * @param int $count
 * @return string|array<int|string, string>|null
 * @param-out int<0, max> $count
 */
function preg_filter(string|array $pattern, string|array $replacement, string|array $subject, int $limit = -1, &$count = null): string|array|null {}

/**
 * @param int $count
 * @param-out mixed $count
 */
function preg_replace_callback_array(array $pattern, string|array $subject, int $limit = -1, &$count = null, int $flags = 0): string|array|null {}

/** @param-out mixed $string */
function sodium_increment(string &$string): void {}

/**
 * @param int $status
 * @param array $resource_usage
 * @param-out mixed $status
 * @param-out mixed $resource_usage
 */
function pcntl_waitpid(int $process_id, &$status, int $flags = 0, &$resource_usage = []): int {}

/**
 * @param array $info
 * @param array $resource_usage
 * @param-out mixed $info
 * @param-out mixed $resource_usage
 */
function pcntl_waitid(int $idtype = P_ALL, ?int $id = null, &$info = [], int $flags = WEXITED, &$resource_usage = []): bool {}

/**
 * @param int $status
 * @param array $resource_usage
 * @param-out mixed $status
 * @param-out mixed $resource_usage
 */
function pcntl_wait(&$status, int $flags = 0, &$resource_usage = []): int {}

/**
 * @param array $old_signals
 * @param-out mixed $old_signals
 */
function pcntl_sigprocmask(int $mode, array $signals, &$old_signals = null): bool {}

/**
 * @param array $info
 * @param-out mixed $info
 */
function pcntl_sigwaitinfo(array $signals, &$info = []): int|false {}

/**
 * @param array $info
 * @param-out mixed $info
 */
function pcntl_sigtimedwait(array $signals, &$info = [], int $seconds = 0, int $nanoseconds = 0): int|false {}

/**
 * @param string $output
 * @param-out mixed $output
 */
function openssl_x509_export(OpenSSLCertificate|string $certificate, &$output, bool $no_text = true): bool {}

/**
 * @param string $output
 * @param OpenSSLAsymmetricKey|OpenSSLCertificate|array|string $private_key
 * @param-out mixed $output
 */
function openssl_pkcs12_export(OpenSSLCertificate|string $certificate, &$output, #[\SensitiveParameter] $private_key, #[\SensitiveParameter] string $passphrase, array $options = []): bool {}

/**
 * @param array $certificates
 * @param-out mixed $certificates
 */
function openssl_pkcs12_read(string $pkcs12, &$certificates, #[\SensitiveParameter] string $passphrase): bool {}

/**
 * @param string $output
 * @param-out mixed $output
 */
function openssl_csr_export(OpenSSLCertificateSigningRequest|string $csr, &$output, bool $no_text = true): bool {}

/**
 * @param OpenSSLAsymmetricKey|null $private_key
 * @param-out mixed $private_key
 */
function openssl_csr_new(array $distinguished_names, #[\SensitiveParameter] &$private_key, ?array $options = null, ?array $extra_attributes = null): OpenSSLCertificateSigningRequest|bool {}

/**
 * @param OpenSSLAsymmetricKey|OpenSSLCertificate|array|string $key
 * @param string $output
 * @param-out mixed $output
 */
function openssl_pkey_export(#[\SensitiveParameter] $key, &$output, #[\SensitiveParameter] ?string $passphrase = null, ?array $options = null): bool {}

/**
 * @param array $certificates
 * @param-out mixed $certificates
 */
function openssl_pkcs7_read(string $data, &$certificates): bool {}

/**
 * @param array $certificates
 * @param-out mixed $certificates
 */
function openssl_cms_read(string $input_filename, &$certificates): bool {}

/**
 * @param string $encrypted_data
 * @param OpenSSLAsymmetricKey|OpenSSLCertificate|array|string $private_key
 * @param-out mixed $encrypted_data
 */
function openssl_private_encrypt(#[\SensitiveParameter] string $data, &$encrypted_data, #[\SensitiveParameter] $private_key, int $padding = OPENSSL_PKCS1_PADDING): bool {}

/**
 * @param string $decrypted_data
 * @param OpenSSLAsymmetricKey|OpenSSLCertificate|array|string $private_key
 * @param-out mixed $decrypted_data
 */
function openssl_private_decrypt(string $data, #[\SensitiveParameter] &$decrypted_data, #[\SensitiveParameter] $private_key, int $padding = OPENSSL_PKCS1_PADDING, ?string $digest_algo = null): bool {}

/**
 * @param string $encrypted_data
 * @param OpenSSLAsymmetricKey|OpenSSLCertificate|array|string $public_key
 * @param-out mixed $encrypted_data
 */
function openssl_public_encrypt(#[\SensitiveParameter] string $data, &$encrypted_data, $public_key, int $padding = OPENSSL_PKCS1_PADDING, ?string $digest_algo = null): bool {}

/**
 * @param string $decrypted_data
 * @param OpenSSLAsymmetricKey|OpenSSLCertificate|array|string $public_key
 * @param-out mixed $decrypted_data
 */
function openssl_public_decrypt(string $data, #[\SensitiveParameter] &$decrypted_data, $public_key, int $padding = OPENSSL_PKCS1_PADDING): bool {}

/**
 * @param string $sealed_data
 * @param array $encrypted_keys
 * @param string $iv
 * @param-out mixed $sealed_data
 * @param-out mixed $encrypted_keys
 * @param-out mixed $iv
 */
function openssl_seal(#[\SensitiveParameter] string $data, &$sealed_data, &$encrypted_keys, array $public_key, string $cipher_algo, &$iv = null): int|false {}

/**
 * @param string $output
 * @param OpenSSLAsymmetricKey|OpenSSLCertificate|array|string $private_key
 * @param-out mixed $output
 */
function openssl_open(string $data, #[\SensitiveParameter] &$output, string $encrypted_key, #[\SensitiveParameter] $private_key, string $cipher_algo, ?string $iv = null): bool {}

/**
 * The tag of an AEAD cipher; the checker narrows it from a cipher named at
 * the call as PHPStan's extension does (openSSLTagType).
 *
 * @param string $tag
 * @param-out non-empty-string|null $tag
 */
function openssl_encrypt(#[\SensitiveParameter] string $data, string $cipher_algo, #[\SensitiveParameter] string $passphrase, int $options = 0, string $iv = "", &$tag = null, ?string $aad = "", int $tag_length = 16): string|false {}

/**
 * @param array $result
 * @param-out ($string is lowercase-string ? array<int|string, array<mixed>|lowercase-string> : array<int|string, array<mixed>|string>) $result
 */
function mb_parse_str(string $string, &$result): bool {}

/**
 * @param array $matches
 * @param-out mixed $matches
 */
#[\Deprecated(since: '8.6', message: "because the underlying library is no longer maintained")]
function mb_ereg(string $pattern, string $string, &$matches = null): bool {}

/**
 * @param array $matches
 * @param-out mixed $matches
 */
#[\Deprecated(since: '8.6', message: "because the underlying library is no longer maintained")]
function mb_eregi(string $pattern, string $string, &$matches = null): bool {}

class SplFileObject extends SplFileInfo implements RecursiveIterator, SeekableIterator
{
    /**
     * @param int $wouldBlock
     * @param-out mixed $wouldBlock
     */
    public function flock(int $operation, &$wouldBlock = null): bool {}

    /** @param-out mixed $vars */
    public function fscanf(string $format, mixed &...$vars): array|int|null {}
}

class IntlTimeZone
{
    /**
     * @param bool $isSystemId
     * @param-out mixed $isSystemId
     */
    public static function getCanonicalID(string $timezoneId, &$isSystemId = null): string|false {}

    /**
     * @param int $rawOffset
     * @param int $dstOffset
     * @param-out mixed $rawOffset
     * @param-out mixed $dstOffset
     */
    public function getOffset(float $timestamp, bool $local, &$rawOffset, &$dstOffset): bool {}
}

class UConverter
{
    /**
     * @param int $error
     * @param-out mixed $error
     */
    public function fromUCallback(int $reason, array $source, int $codePoint, &$error): string|int|array|null {}

    /**
     * @param int $error
     * @param-out mixed $error
     */
    public function toUCallback(int $reason, string $source, string $codeUnits, &$error): string|int|array|null {}
}

class NumberFormatter
{
    /**
     * @param string $currency
     * @param int $offset
     * @param-out mixed $currency
     */
    public function parseCurrency(string $string, &$currency, &$offset = null): float|false {}
}

class IntlDateFormatter
{
    /**
     * @param int $offset
     * @param-out mixed $offset
     */
    public function parse(string $string, &$offset = null): int|float|false {}

    /**
     * @param int $offset
     * @param-out mixed $offset
     */
    public function parseToCalendar(string $string, &$offset = null): int|float|false {}

    /**
     * @param int $offset
     * @return array<string, int>|false
     * @param-out mixed $offset
     */
    public function localtime(string $string, &$offset = null): array|false {}
}

class Spoofchecker
{
    /**
     * @param int $errorCode
     * @param-out mixed $errorCode
     */
    public function isSuspicious(string $string, &$errorCode = null): bool {}

    /**
     * @param int $errorCode
     * @param-out mixed $errorCode
     */
    public function areConfusable(string $string1, string $string2, &$errorCode = null): bool {}

    /**
     * @param int $errorCode
     * @param-out mixed $errorCode
     */
    public function areBidiConfusable(int $direction, string $string1, string $string2, &$errorCode = null): bool {}
}

class PDOStatement implements IteratorAggregate
{
    /** @param-out mixed $var */
    public function bindColumn(string|int $column, mixed &$var, int $type = PDO::PARAM_STR, int $maxLength = 0, mixed $driverOptions = null): bool {}

    /** @param-out mixed $var */
    public function bindParam(string|int $param, mixed &$var, int $type = PDO::PARAM_STR, int $maxLength = 0, mixed $driverOptions = null): bool {}
}

class SoapClient
{
    /**
     * @param SoapHeader|array|null $inputHeaders
     * @param array $outputHeaders
     * @param-out mixed $outputHeaders
     */
    public function __soapCall(string $name, array $args, ?array $options = null, $inputHeaders = null, &$outputHeaders = null): mixed {}
}

class ZipArchive implements Countable
{
    /**
     * @param int $opsys
     * @param int $attr
     * @param-out mixed $opsys
     * @param-out mixed $attr
     */
    public function getExternalAttributesName(string $name, &$opsys, &$attr, int $flags = 0): bool {}

    /**
     * @param int $opsys
     * @param int $attr
     * @param-out mixed $opsys
     * @param-out mixed $attr
     */
    public function getExternalAttributesIndex(int $index, &$opsys, &$attr, int $flags = 0): bool {}
}

class mysqli
{
    /**
     * @param-out mixed $read
     * @param-out mixed $error
     * @param-out mixed $reject
     */
    public static function poll(?array &$read, ?array &$error, array &$reject, int $seconds, int $microseconds = 0): int|false {}
}

class mysqli_stmt
{
    /** @param-out mixed $vars */
    public function bind_result(mixed &...$vars): bool {}
}
