# Security policy

## Reporting a vulnerability

Please do not report a vulnerability in a public issue. Report it privately through GitHub's
private vulnerability reporting: the repository's **Security** tab, **Report a vulnerability**
(<https://github.com/phaenonlang/phaenon/security/advisories/new>).

Say what is affected (the compiler `phn`, the code it emits, the `phaenon/phn` Composer plugin and
the binary it downloads, the release binaries), the version (`phn version`, or the plugin's from
`composer.lock`), and the input or steps that show the problem. The fix is coordinated in the
advisory, and the advisory is published with the release that carries it.

## Supported versions

Phaenon is pre-alpha. Security fixes go into the latest minor release line only; upgrade to its
newest patch release to receive them.
