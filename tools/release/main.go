// Command release builds phn's release binaries and pins their checksums
// (design/tooling.md §3.2): one static binary per platform, built so that
// the same sources give the same bytes anywhere — exactly the toolchain
// go.mod's toolchain line names (GOTOOLCHAIN, since the line alone is only
// a minimum: a newer local Go would build instead), -trimpath, no VCS
// stamp, no build ID, the version set by the linker. The checksums go into composer/checksums.json, which the
// phaenon/phn plugin verifies every download against, so composer.lock pins
// the binary through the package.
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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// asset is a binary's entry in checksums.json.
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
	for _, p := range platforms {
		file := "phn-" + p.key
		if p.goos == "windows" {
			file += ".exe"
		}
		dst := filepath.Join(*out, file)
		cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false",
			"-ldflags", "-s -w -buildid= -X main.version="+*version, "-o", dst, "./cmd/phn")
		cmd.Env = append(os.Environ(), "GOTOOLCHAIN="+toolchain, "CGO_ENABLED=0", "GOOS="+p.goos, "GOARCH="+p.goarch, "GOFLAGS=", "GOAMD64=v1", "GOARM64=v8.0")
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if err := cmd.Run(); err != nil {
			fail("release: building %s: %v", p.key, err)
		}
		data, err := os.ReadFile(dst)
		if err != nil {
			fail("%v", err)
		}
		sum := sha256.Sum256(data)
		built.Assets[p.key] = asset{File: file, SHA256: hex.EncodeToString(sum[:])}
		fmt.Printf("%s  %s\n", hex.EncodeToString(sum[:]), file)
	}
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
			fail("release: the binaries built differ from %s, which the release PR pinned:\n%s", checksumsPath, diff)
		}
		fmt.Printf("%d binaries match %s\n", len(built.Assets), checksumsPath)
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
