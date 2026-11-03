// Package distribution tests the Composer package phaenon/phn that the
// repository root is (composer.json and composer/, design/tooling.md §3): the
// plugin that builds a project's Phaenon code when Composer writes the
// autoloader, the pinned binary it downloads, verifies, caches and installs,
// and the vendor/bin/phn launcher.
//
// The tests run a real Composer, named by PHAENON_COMPOSER, and are skipped
// without it. Each one builds temporary root projects that install a copy of
// the package, so that a test can pin binaries of its own in the copy's
// checksums.json, and the phaenon/runtime checkout beside the repository, both
// through path repositories with Packagist switched off. COMPOSER_HOME and the
// cache live in the test's directory. A shell script stands in for phn: it
// records where and how it ran, and the tests serve it as the release asset
// when they exercise the download.
//
// The package holds no Go code; it exists for its tests.
package distribution
