package distribution

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// world is what the projects of one test share: a copy of the phaenon/phn
// package, the fake phn, Composer's home and cache.
type world struct {
	t        *testing.T
	composer string
	base     string // the test's directory, symlinks resolved
	plugin   string // the copy of the package the projects install
	home     string // COMPOSER_HOME
	cache    string // COMPOSER_CACHE_DIR
	fake     string // the fake phn
	log      string // the fake's record of its runs
}

// newWorld prepares a test's world. The fake phn exits with status 2 when it
// is run with exactly the arguments failOn, unless that is empty.
func newWorld(t *testing.T, failOn string) *world {
	t.Helper()
	composer := os.Getenv("PHAENON_COMPOSER")
	if composer == "" {
		t.Skip("PHAENON_COMPOSER names no composer binary")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the fake phn is a shell script")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &world{
		t:        t,
		composer: composer,
		base:     base,
		plugin:   filepath.Join(base, "phaenon-phn"),
		home:     filepath.Join(base, "composer-home"),
		cache:    filepath.Join(base, "composer-cache"),
		fake:     filepath.Join(base, "fake", "phn"),
		log:      filepath.Join(base, "fake", "runs"),
	}
	root := repositoryRoot(t)
	copyFile(t, filepath.Join(root, "composer.json"), filepath.Join(w.plugin, "composer.json"))
	copyTree(t, filepath.Join(root, "composer"), filepath.Join(w.plugin, "composer"))
	// The repository pins its last release; a world starts with no release
	// pinned, as the repository did before the first one, and a test that
	// wants one pins it.
	writeJSON(t, filepath.Join(w.plugin, "composer", "checksums.json"), map[string]any{"version": nil, "assets": map[string]any{}})
	writeFile(t, w.fake, fakePhn(w.log, failOn), 0o755)
	return w
}

// fakePhn is a shell script standing in for phn. It records each run as a
// line of the log, the physical working directory and then the arguments,
// separated by tabs; it keeps a copy of vendor/composer/autoload_psr4.php as
// it found it, as log.psr4.N for the Nth run. `version` and `exit N` answer
// for the launcher's tests; `build` writes an output, as phn would.
func fakePhn(log, failOn string) []byte {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	fmt.Fprintf(&b, "log=%s\n", shellQuote(log))
	b.WriteString(`n=$(( $(cat "$log" 2>/dev/null | wc -l) + 1 ))
{ pwd -P | tr -d '\n'; for a in "$@"; do printf '\t%s' "$a"; done; echo; } >> "$log"
if [ -f vendor/composer/autoload_psr4.php ]; then cp vendor/composer/autoload_psr4.php "$log.psr4.$n"; fi
case "$1" in
version) echo "phn fake"; exit 0 ;;
exit) echo out; echo err >&2; exit "$2" ;;
esac
`)
	if failOn != "" {
		fmt.Fprintf(&b, "if [ \"$*\" = %s ]; then echo 'src/Greeter.phn:3:5: error P2103: a finding of the fake' >&2; exit 2; fi\n", shellQuote(failOn))
	}
	b.WriteString(`if [ "$*" = build ]; then mkdir -p .src && printf '<?php\n\nnamespace App;\n\nfinal class Greeter\n{\n}\n' > .src/Greeter.php; fi
echo "phn $*: done"
echo "phn $*: warning" >&2
`)
	return []byte(b.String())
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// fakeBytes is the fake phn as the release asset the tests serve.
func (w *world) fakeBytes() []byte {
	data, err := os.ReadFile(w.fake)
	if err != nil {
		w.t.Fatal(err)
	}
	return data
}

// pin writes the package copy's composer/checksums.json for the platform the
// test runs on, as the release tooling would: beside it the playground's
// assets, whose keys name no platform, so that every install shows the
// plugin passing them over.
func (w *world) pin(version, sha string) {
	w.t.Helper()
	other := strings.Repeat("0", 64)
	checksums := map[string]any{
		"version": version,
		"assets": map[string]any{
			platformKey(): map[string]string{"file": assetFile(), "sha256": sha},
			"wasm":        map[string]string{"file": "phn.wasm.gz", "sha256": other},
			"wasm-exec":   map[string]string{"file": "wasm_exec.js", "sha256": other},
		},
	}
	writeJSON(w.t, filepath.Join(w.plugin, "composer", "checksums.json"), checksums)
}

// checkout makes the package copy a checkout of the compiler: it holds the Go
// module, and the fake phn as the developer's build in bin/ when build is set.
func (w *world) checkout(build bool) {
	w.t.Helper()
	writeFile(w.t, filepath.Join(w.plugin, "go.mod"), []byte("module github.com/phaenonlang/phaenon\n\ngo 1.27\n"), 0o644)
	if build {
		copyFile(w.t, w.fake, filepath.Join(w.plugin, "bin", "phn"))
	}
}

// run is one run of the fake phn.
type run struct {
	dir  string
	args []string
}

func (r run) String() string {
	return r.dir + ": " + strings.Join(r.args, " ")
}

// runs returns the fake's runs, oldest first.
func (w *world) runs() []run {
	w.t.Helper()
	data, err := os.ReadFile(w.log)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		w.t.Fatal(err)
	}
	var runs []run
	for line := range strings.Lines(string(data)) {
		fields := strings.Split(strings.TrimSuffix(line, "\n"), "\t")
		runs = append(runs, run{dir: fields[0], args: fields[1:]})
	}
	return runs
}

// mapsAtRun returns vendor/composer/autoload_psr4.php as the Nth run (from
// 1) found it, failing the test when it did not exist.
func (w *world) mapsAtRun(n int) string {
	w.t.Helper()
	data, err := os.ReadFile(fmt.Sprintf("%s.psr4.%d", w.log, n))
	if err != nil {
		w.t.Fatalf("run %d found no vendor/composer/autoload_psr4.php: %v", n, err)
	}
	return string(data)
}

// expectRuns fails the test unless the fake ran exactly as given, each run
// being a project and its arguments.
func (w *world) expectRuns(want ...run) {
	w.t.Helper()
	got := w.runs()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		w.t.Errorf("phn ran\n\t%v\nwant\n\t%v", got, want)
	}
}

// project describes a root project.
type project struct {
	w   *world
	dir string

	phaenonJSON   bool           // the root has a phaenon.json
	extra         map[string]any // the root's extra
	config        map[string]any // added to the root's config
	libraries     []string       // Phaenon libraries the root requires, such as "acme/lib"
	packages      []plainPackage // packages of plain PHP the root requires, each at its version
	dependencies  []dependency   // further packages the root requires, each from its repository
	withoutPlugin bool           // the root does not require phaenon/phn: a project of plain PHP
	mirror        *mirror        // binary-url and auth.json for the mirror
	baseURL       bool           // binary-url as a base URL rather than a template
	override      bool           // PHN_BINARY names the fake
	symlinkPlugin bool           // the package is symlinked into vendor, not copied
	binary        string         // PHN_BINARY names this binary, a real phn, instead
}

// newProject writes the root project p describes into a directory of its own.
func (w *world) newProject(name string, p project) *project {
	w.t.Helper()
	p.w = w
	p.dir = filepath.Join(w.base, name)
	p.write()
	return &p
}

// write writes the root's composer.json, and its phaenon.json and auth.json
// when it has them; the libraries and packages it requires are written
// beside the world.
func (p *project) write() {
	t := p.w.t
	t.Helper()
	// With Packagist switched off, the plugin's own package is all there is
	// to install: it requires none (design/tooling.md T16).
	repositories := []any{map[string]any{"packagist.org": false}}
	require := map[string]string{}
	config := map[string]any{}
	if !p.withoutPlugin {
		repositories = append(repositories, pathRepository(p.w.plugin, "phaenon/phn", "0.1.x-dev", p.symlinkPlugin))
		require["phaenon/phn"] = "*"
		config["allow-plugins"] = map[string]bool{"phaenon/phn": true}
	}
	for _, d := range p.dependencies {
		repositories = append(repositories, d.repository)
		require[d.name] = d.constraint
	}
	for _, name := range p.libraries {
		repositories = append(repositories, pathRepository(p.w.library(name), name, "1.0.0", false))
		require[name] = "*"
	}
	for _, pkg := range p.packages {
		repositories = append(repositories, pathRepository(p.w.plainPackage(pkg), pkg.name, pkg.version, false))
		require[pkg.name] = pkg.version
	}
	for key, value := range p.config {
		config[key] = value
	}
	extra := map[string]any{}
	for key, value := range p.extra {
		extra[key] = value
	}
	if p.mirror != nil {
		config["secure-http"] = false
		url := p.mirror.registry.URL + "/phn/{version}/{file}"
		if p.baseURL {
			url = p.mirror.registry.URL + "/phn/{version}"
		}
		extra["phaenon"] = merge(extra["phaenon"], map[string]any{"binary-url": url})
		writeJSON(t, filepath.Join(p.dir, "auth.json"), map[string]any{
			"http-basic": map[string]any{
				p.mirror.origin(): map[string]string{"username": mirrorUser, "password": mirrorPassword},
			},
		})
	}
	manifest := map[string]any{
		"name":              "acme/app",
		"minimum-stability": "dev",
		"prefer-stable":     true,
		"repositories":      repositories,
		"require":           require,
		"autoload":          map[string]any{"psr-4": map[string]string{`App\`: ".src/"}},
		"config":            config,
	}
	if len(extra) > 0 {
		manifest["extra"] = extra
	}
	writeJSON(t, filepath.Join(p.dir, "composer.json"), manifest)
	if p.phaenonJSON {
		writeJSON(t, filepath.Join(p.dir, "phaenon.json"), map[string]any{"roots": map[string]string{"src": ".src"}})
		writeFile(t, filepath.Join(p.dir, "src", "Greeter.phn"), []byte("namespace App;\n\nfinal class Greeter\n{\n}\n"), 0o644)
	}
}

// library writes a Phaenon library, a package that ships its .phn sources
// and a phaenon.json, and returns its directory. "acme/lib" has the
// namespace Acme\Lib\, autoloaded from its output directory .src.
func (w *world) library(name string) string {
	w.t.Helper()
	dir := filepath.Join(w.base, "libraries", name)
	vendor, pkg, _ := strings.Cut(name, "/")
	namespace := title(vendor) + `\` + title(pkg) + `\`
	writeJSON(w.t, filepath.Join(dir, "composer.json"), map[string]any{
		"name":     name,
		"autoload": map[string]any{"psr-4": map[string]string{namespace: ".src/"}},
	})
	writeJSON(w.t, filepath.Join(dir, "phaenon.json"), map[string]any{"roots": map[string]string{"src": ".src"}})
	writeFile(w.t, filepath.Join(dir, "src", "Thing.phn"), []byte("namespace "+strings.TrimSuffix(namespace, `\`)+";\n\nfinal class Thing\n{\n}\n"), 0o644)
	return dir
}

// dependency is a package a root requires from a repository of its own.
type dependency struct {
	name, constraint string
	repository       map[string]any
}

// plainPackage is a package of plain PHP: a composer.json and nothing
// else, served by a path repository at version.
type plainPackage struct {
	name, version string
	manifest      map[string]any // the rest of its composer.json: autoload, replace…
}

// plainPackage writes pkg beside the world and returns its directory.
func (w *world) plainPackage(pkg plainPackage) string {
	w.t.Helper()
	dir := filepath.Join(w.base, "packages", pkg.name)
	writeJSON(w.t, filepath.Join(dir, "composer.json"), merge(pkg.manifest, map[string]any{"name": pkg.name}))
	return dir
}

func title(s string) string {
	return strings.ToUpper(s[:1]) + s[1:]
}

func pathRepository(url, name, version string, symlink bool) map[string]any {
	return map[string]any{
		"type": "path",
		"url":  url,
		"options": map[string]any{
			"symlink":  symlink,
			"versions": map[string]string{name: version},
		},
	}
}

func merge(base any, add map[string]any) map[string]any {
	merged := map[string]any{}
	if m, ok := base.(map[string]any); ok {
		for key, value := range m {
			merged[key] = value
		}
	}
	for key, value := range add {
		merged[key] = value
	}
	return merged
}

// composer runs Composer in the project and returns what it wrote to both
// streams.
func (p *project) composer(args ...string) (string, error) {
	cmd := exec.Command(p.w.composer, args...)
	cmd.Dir = p.dir
	cmd.Env = p.environ()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// mustComposer runs Composer and fails the test when it fails.
func (p *project) mustComposer(args ...string) string {
	p.w.t.Helper()
	out, err := p.composer(args...)
	if err != nil {
		p.w.t.Fatalf("composer %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// launch runs vendor/bin/phn in the project, through `php` and its
// arguments when php is given.
func (p *project) launch(php []string, args ...string) (stdout, stderr string, status int) {
	p.w.t.Helper()
	command := append(php, filepath.Join(p.dir, "vendor", "bin", "phn"))
	cmd := exec.Command(command[0], append(command[1:], args...)...)
	cmd.Dir = p.dir
	cmd.Env = p.environ()
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		status = exit.ExitCode()
	case err != nil:
		p.w.t.Fatal(err)
	}
	return out.String(), errOut.String(), status
}

// environ is the test process's environment without anything that would
// point Composer or the plugin elsewhere, plus the world's.
func (p *project) environ() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "COMPOSER") || strings.HasPrefix(kv, "PHN_BINARY=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"COMPOSER_HOME="+p.w.home,
		"COMPOSER_CACHE_DIR="+p.w.cache,
		"COMPOSER_NO_INTERACTION=1",
		"COMPOSER_NO_AUDIT=1",
		"COMPOSER_ROOT_VERSION=1.0.0",
	)
	switch {
	case p.binary != "":
		env = append(env, "PHN_BINARY="+p.binary)
	case p.override:
		env = append(env, "PHN_BINARY="+p.w.fake)
	}
	return env
}

// installed is where the plugin installs the binary in the project.
func (p *project) installed() string {
	return filepath.Join(p.dir, "vendor", "phaenon", "phn", "bin", "phn")
}

// platformKey names the platform the tests run on as checksums.json does.
func platformKey() string {
	return runtime.GOOS + "-" + runtime.GOARCH
}

func assetFile() string {
	return "phn-" + platformKey()
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// repositoryRoot is the root of this repository, the phaenon/phn package.
func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, append(data, '\n'), 0o644)
}

func writeFile(t *testing.T, path string, data []byte, mode fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	info, err := os.Stat(from)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, to, data, info.Mode().Perm())
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		copyFile(t, path, filepath.Join(to, rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
