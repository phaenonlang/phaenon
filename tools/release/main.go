// Command release builds phn's release binaries and pins their checksums
// (design/tooling.md §3.2): one static binary per platform, built so that
// the same sources give the same bytes anywhere — exactly the toolchain
// go.mod's toolchain line names (GOTOOLCHAIN, since the line alone is only
// a minimum: a newer local Go would build instead), -trimpath, no VCS
// stamp, no build ID, the version set by the linker. The checksums go into composer/checksums.json, which the
// phaenon/phn plugin verifies every download against, so composer.lock pins
// the binary through the package.
//
// Beside the binaries go the playground's two assets (§3.6): phn.wasm.gz,
// cmd/phn-wasm built the same way and gzipped with a header that names no
// file, time or system, and wasm_exec.js, the loader of the toolchain that
// built it. They are pinned in the same file under keys no platform has,
// which the plugin never asks for.
//
//	go run ./tools/release -version 0.1.0 -out dist           # build, write the checksums
//	go run ./tools/release -version 0.1.0 -out dist -verify   # build, refuse unless they match
//
// The release PR's workflow runs the first and commits the checksums; the
// tag's workflow runs the second before it publishes the binaries. Both
// keep the lockstep of design/tooling.md §3.4: composer.json requires
// phaenon/runtime, and suggests phaenon/concurrent, at the release's minor
// version, which the first writes and the second checks.
package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
)

// platform is one release asset: its key in checksums.json and Go's names.
type platform struct {
	key, goos, goarch string
}

var platforms = []platform{
	{"darwin-amd64", "darwin", "amd64"},
	{"darwin-arm64", "darwin", "arm64"},
	{"linux-amd64", "linux", "amd64"},
	{"linux-arm64", "linux", "arm64"},
	{"windows-amd64", "windows", "amd64"},
}

// The keys of the playground's assets in checksums.json: not os-arch, so
// that no platform the plugin runs on picks them.
const (
	wasmKey     = "wasm"      // phn.wasm.gz
	wasmExecKey = "wasm-exec" // wasm_exec.js
)

// asset is a release file's entry in checksums.json.
type asset struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}

type checksums struct {
	Version *string          `json:"version"`
	Assets  map[string]asset `json:"assets"`
}

const checksumsPath = "composer/checksums.json"

// lockstep finds the version constraints of the runtime packages in
// composer.json: the requirement and the suggestion's leading constraint.
var lockstep = regexp.MustCompile(`("phaenon/(?:runtime|concurrent)":\s*")(\d+\.\d+\.\*)`)

var toolchainLine = regexp.MustCompile(`(?m)^toolchain (go\d+\.\d+\.\d+)$`)

var semver = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?$`)

func main() {
	version := flag.String("version", "", "the release, X.Y.Z")
	out := flag.String("out", "dist", "the directory the binaries are written to")
	verify := flag.Bool("verify", false, "compare with "+checksumsPath+" instead of writing it")
	flag.Parse()
	if !semver.MatchString(*version) {
		fail("release: -version must be X.Y.Z, not %q", *version)
	}
	gomod, err := os.ReadFile("go.mod")
	if err != nil {
		fail("release: %v", err)
	}
	tc := toolchainLine.FindSubmatch(gomod)
	if tc == nil {
		fail("release: go.mod names no toolchain goX.Y.Z to build with")
	}
	toolchain := string(tc[1])
	// The wasm's gzip is written by this process, and compress/flate's
	// output is its toolchain's: run by another Go, the tool runs again
	// under the pinned one, so that the .gz is as reproducible as the
	// binaries.
	if runtime.Version() != toolchain && os.Getenv("GOTOOLCHAIN") != toolchain {
		cmd := exec.Command("go", append([]string{"run", "./tools/release"}, os.Args[1:]...)...)
		cmd.Env = append(os.Environ(), "GOTOOLCHAIN="+toolchain)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				os.Exit(exit.ExitCode())
			}
			fail("release: running under %s: %v", toolchain, err)
		}
		return
	}
	fmt.Printf("toolchain %s\n", toolchain)
	parts := strings.SplitN(*version, ".", 3)
	minor := parts[0] + "." + parts[1] + ".*"
	manifest, err := os.ReadFile("composer.json")
	if err != nil {
		fail("release: %v", err)
	}
	found := lockstep.FindAllSubmatch(manifest, -1)
	if len(found) == 0 {
		fail("release: composer.json names no phaenon/runtime constraint X.Y.*")
	}
	for _, m := range found {
		if *verify && string(m[2]) != minor {
			fail("release: composer.json has %s%s\", the release needs %s", m[1], m[2], minor)
		}
	}
	if !*verify {
		manifest = lockstep.ReplaceAll(manifest, []byte("${1}"+minor))
		if err := os.WriteFile("composer.json", manifest, 0o644); err != nil {
			fail("%v", err)
		}
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail("%v", err)
	}
	built := checksums{Version: version, Assets: map[string]asset{}}
	pin := func(key, file string, data []byte) {
		sum := sha256.Sum256(data)
		built.Assets[key] = asset{File: file, SHA256: hex.EncodeToString(sum[:])}
		fmt.Printf("%s  %s\n", hex.EncodeToString(sum[:]), file)
	}
	// goEnv is the environment every go command runs in: the pinned
	// toolchain, and every setting that would change the bytes fixed.
	goEnv := slices.Clip(append(os.Environ(), "GOTOOLCHAIN="+toolchain, "CGO_ENABLED=0", "GOFLAGS=", "GOAMD64=v1", "GOARM64=v8.0", "GOWASM="))
	build := func(goos, goarch, dst, pkg string) {
		cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false",
			"-ldflags", "-s -w -buildid= -X main.version="+*version, "-o", dst, pkg)
		cmd.Env = append(goEnv, "GOOS="+goos, "GOARCH="+goarch)
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if err := cmd.Run(); err != nil {
			fail("release: building %s for %s-%s: %v", pkg, goos, goarch, err)
		}
	}
	for _, p := range platforms {
		file := "phn-" + p.key
		if p.goos == "windows" {
			file += ".exe"
		}
		dst := filepath.Join(*out, file)
		build(p.goos, p.goarch, dst, "./cmd/phn")
		data, err := os.ReadFile(dst)
		if err != nil {
			fail("%v", err)
		}
		pin(p.key, file, data)
	}
	// The raw wasm is not an asset: phaenon.dev serves its downloads as
	// published, without transfer compression, so the page fetches the gzip
	// and inflates it (DecompressionStream). It is built outside out, which
	// the tag's workflow uploads whole.
	tmp, err := os.MkdirTemp("", "phn-release-")
	if err != nil {
		fail("%v", err)
	}
	build("js", "wasm", filepath.Join(tmp, "phn.wasm"), "./cmd/phn-wasm")
	raw, err := os.ReadFile(filepath.Join(tmp, "phn.wasm"))
	os.RemoveAll(tmp)
	if err != nil {
		fail("%v", err)
	}
	var gz bytes.Buffer
	zw, err := gzip.NewWriterLevel(&gz, gzip.BestCompression)
	if err != nil {
		fail("%v", err)
	}
	// No name, a zero time, the "unknown" system: the header says nothing
	// about the machine or the moment of the build.
	zw.Header = gzip.Header{OS: 255}
	if _, err := zw.Write(raw); err != nil {
		fail("%v", err)
	}
	if err := zw.Close(); err != nil {
		fail("%v", err)
	}
	if err := os.WriteFile(filepath.Join(*out, "phn.wasm.gz"), gz.Bytes(), 0o644); err != nil {
		fail("%v", err)
	}
	pin(wasmKey, "phn.wasm.gz", gz.Bytes())
	fmt.Printf("phn.wasm: %d bytes, %d gzipped\n", len(raw), gz.Len())
	// wasm_exec.js is the runtime glue of the toolchain that built the wasm,
	// and only that one's fits it.
	cmd := exec.Command("go", "env", "GOROOT")
	cmd.Env = goEnv
	goroot, err := cmd.Output()
	if err != nil {
		fail("release: go env GOROOT: %v", err)
	}
	glue, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(goroot)), "lib", "wasm", "wasm_exec.js"))
	if err != nil {
		fail("release: %v", err)
	}
	if err := os.WriteFile(filepath.Join(*out, "wasm_exec.js"), glue, 0o644); err != nil {
		fail("%v", err)
	}
	pin(wasmExecKey, "wasm_exec.js", glue)
	if *verify {
		data, err := os.ReadFile(checksumsPath)
		if err != nil {
			fail("release: %v", err)
		}
		var pinned checksums
		if err := json.Unmarshal(data, &pinned); err != nil {
			fail("release: %s: %v", checksumsPath, err)
		}
		if diff := compare(pinned, built); diff != "" {
			fail("release: the assets built differ from %s, which the release PR pinned:\n%s", checksumsPath, diff)
		}
		fmt.Printf("%d assets match %s\n", len(built.Assets), checksumsPath)
		return
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "    ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(built); err != nil {
		fail("%v", err)
	}
	if err := os.WriteFile(checksumsPath, buf.Bytes(), 0o644); err != nil {
		fail("%v", err)
	}
	fmt.Printf("%s written\n", checksumsPath)
}

// compare lists how built differs from pinned, "" when it does not.
func compare(pinned, built checksums) string {
	var diff bytes.Buffer
	if pinned.Version == nil || *pinned.Version != *built.Version {
		v := "null"
		if pinned.Version != nil {
			v = *pinned.Version
		}
		fmt.Fprintf(&diff, "  version: pinned %s, built %s\n", v, *built.Version)
	}
	keys := map[string]bool{}
	for k := range pinned.Assets {
		keys[k] = true
	}
	for k := range built.Assets {
		keys[k] = true
	}
	var sorted []string
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		if p, b := pinned.Assets[k], built.Assets[k]; p != b {
			fmt.Fprintf(&diff, "  %s: pinned %s %s, built %s %s\n", k, p.File, p.SHA256, b.File, b.SHA256)
		}
	}
	return diff.String()
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
