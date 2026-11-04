package main

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/stdlib"
)

// share is what the programs of a long-running phn share: phn lsp builds a
// program for every edit, phn watch for every change. nil in a command that
// builds one program and exits, which then reads everything afresh.
var share *programShare

// programShare keeps, between programs:
//   - the parsed files, whose trees programs can share (program.ParseCache);
//   - the extensions php reported: running php is a third of a second;
//   - each vendor/composer directory's maps, read again when one of its
//     files changes.
type programShare struct {
	parses *program.ParseCache

	mu      sync.Mutex
	exts    *program.Extensions
	loaders map[string]sharedLoader // a vendor/composer directory → its loader
}

type sharedLoader struct {
	loader *program.ComposerLoader
	stamp  time.Time // the newest modification time of the directory's files
}

func newProgramShare() *programShare {
	return &programShare{parses: program.NewParseCache(), loaders: map[string]sharedLoader{}}
}

// extensions reads php's extensions on first use.
func (s *programShare) extensions(php, cacheDir string) *program.Extensions {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exts == nil {
		embedded := stdlib.EmbeddedExtensions()
		s.exts = program.ReadExtensions(php, cacheDir, func(lower string) bool { return embedded[lower] })
	}
	return s.exts
}

// composer returns the loader of the vendor/composer directory found from
// dir (or composerDir), its Sources set, read again when a file of the
// directory is newer than when it was read: `composer install` and
// `dump-autoload` rewrite them.
func (s *programShare) composer(dir string) (*program.ComposerLoader, error) {
	found := composerDir
	if found == "" {
		var err error
		if found, err = program.FindComposerDir(dir); err != nil {
			return nil, err
		}
	}
	stamp := newest(found)
	s.mu.Lock()
	defer s.mu.Unlock()
	if cached, ok := s.loaders[found]; ok && cached.stamp.Equal(stamp) {
		return cached.loader, nil
	}
	l, err := program.LoadComposer(found)
	if err != nil {
		return nil, err
	}
	setSources(l)
	s.loaders[found] = sharedLoader{loader: l, stamp: stamp}
	return l, nil
}

// setSources maps a loader's output directories to the sources behind them:
// the project's roots and the installed Phaenon packages' (newProgram).
func setSources(l *program.ComposerLoader) {
	l.Sources = map[string]string{}
	for out, src := range installedSources(l.Dir) {
		l.Sources[out] = src
	}
	for out, src := range projectSources {
		l.Sources[out] = src
	}
}

// newest is the latest modification time among a directory's files.
func newest(dir string) time.Time {
	var t time.Time
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if info, err := os.Stat(filepath.Join(dir, e.Name())); err == nil && info.ModTime().After(t) {
			t = info.ModTime()
		}
	}
	return t
}
