// Package distribution tests the Composer package phaenon/phn that the
// repository root is (composer.json and composer/, design/tooling.md §3): the
// plugin that builds a project's Phaenon code when Composer writes the
// autoloader, the pinned binary it downloads, verifies, caches and installs,
// and the vendor/bin/phn launcher.
//
// The tests run a real Composer, named by PHAENON_COMPOSER, and are skipped
// without it. Each one builds temporary root projects that install a copy of
// the package, so that a test can pin binaries of its own in the copy's
// checksums.json, through a path repository with Packagist switched off: the
// package requires no other (design/tooling.md T16), and the projects install
// nothing else of Phaenon's. COMPOSER_HOME and the cache live in the test's
// directory. A shell script stands in for phn: it records where and how it
// ran, and the tests serve it as the release asset when they exercise the
// download. One test builds the real phn instead, and runs what it compiles.
//
// A library that commits its outputs (design/roadmap.md D28) is a fixture
// of its own: a git repository the real phn built, installed from the
// archive git makes of it and as a path repository, by projects with the
// plugin and without it (committed_test.go). A library that goes from
// shipping its sources to committing its outputs is another: a repository
// with a tag for each, upgraded as a git checkout and as an archive
// (upgrade_test.go). TestProjectFileReadersAgree holds the plugin's reader
// of phaenon.json to the compiler's reference, and needs php alone, as do
// TestCommittedOutputChecksAgree and TestEarlierBuildsAgree for its check
// of committed outputs and its removal of what an earlier build left.
//
// The package holds no Go code; it exists for its tests.
package distribution
