package distribution

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestMainBranchBesideALibrarysConflict: a project that requires the
// compiler's main branch, phaenon/phn dev-main, installs a library that
// keeps out the releases from before "committed" with the `conflict` its
// own build asks for (design/tooling.md §3.7). Composer takes a branch for
// the version composer.json's branch alias gives it, and a conflict that
// holds the alias holds the branch: the main branch reads the key, so its
// alias stands at the release the key comes in or above it. With the alias
// one line below, the same project is refused, which is what the alias
// would do to every project on the main branch were a key added without
// moving it (TestBranchAliasCoversTheKeys in cmd/phn).
func TestMainBranchBesideALibrarysConflict(t *testing.T) {
	w := newWorld(t, "")
	kit := plainPackage{"acme/kit", "1.0.0", map[string]any{"conflict": map[string]string{"phaenon/phn": "<0.5.0"}}}
	app := func(name string) *project {
		p := &project{w: w, dir: filepath.Join(w.base, name), override: true}
		writeJSON(t, filepath.Join(p.dir, "composer.json"), map[string]any{
			"name":              "acme/app",
			"minimum-stability": "dev",
			"repositories": []any{
				map[string]any{"packagist.org": false},
				pathRepository(w.plugin, "phaenon/phn", "dev-main", false),
				pathRepository(w.plainPackage(kit), kit.name, kit.version, false),
			},
			"require": map[string]string{"phaenon/phn": "dev-main", kit.name: kit.version},
			"config":  map[string]any{"allow-plugins": map[string]bool{"phaenon/phn": true}},
		})
		return p
	}

	onMain := app("main")
	onMain.mustComposer("install")
	if got := onMain.installedPackages(); !slices.Equal(got, []string{"acme/kit", "phaenon/phn"}) {
		t.Errorf("installed %v, want acme/kit and phaenon/phn", got)
	}

	manifest := filepath.Join(w.plugin, "composer.json")
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	alias := regexp.MustCompile(`("dev-main":\s*")\d+\.\d+\.x-dev`)
	if len(alias.FindAll(data, -1)) != 1 {
		t.Fatalf("composer.json names no one branch alias of dev-main:\n%s", data)
	}
	writeFile(t, manifest, alias.ReplaceAll(data, []byte("${1}0.4.x-dev")), 0o644)
	behind := app("behind")
	out, err := behind.composer("install")
	if err == nil || !strings.Contains(out, "conflicts with phaenon/phn") {
		t.Errorf("composer install with dev-main aliased to 0.4.x-dev: %v, want a refusal for the conflict\n%s", err, out)
	}
}
