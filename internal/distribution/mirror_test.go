package distribution

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

const (
	mirrorUser     = "ci"
	mirrorPassword = "s3cret"
)

// mirror serves phn release assets as a private package registry would, and as
// GitHub does: the registry asks for HTTP basic auth and redirects to a
// storage host, another origin, which must not receive the credentials.
type mirror struct {
	registry *httptest.Server
	storage  *httptest.Server

	mu        sync.Mutex
	assets    map[string][]byte // by path: /0.1.0/phn-darwin-arm64
	downloads int               // asset bodies the storage served
	leaked    bool              // the storage received an Authorization header
}

func newMirror(t *testing.T) *mirror {
	m := &mirror{assets: map[string][]byte{}}
	m.storage = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if r.Header.Get("Authorization") != "" {
			m.leaked = true
		}
		asset, ok := m.assets[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		m.downloads++
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(asset)
	}))
	m.registry = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != mirrorUser || password != mirrorPassword {
			w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
			http.Error(w, "credentials required", http.StatusUnauthorized)
			return
		}
		path, found := strings.CutPrefix(r.URL.Path, "/phn")
		if !found {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, m.storage.URL+path, http.StatusFound)
	}))
	t.Cleanup(m.registry.Close)
	t.Cleanup(m.storage.Close)
	return m
}

// serve publishes an asset of a release.
func (m *mirror) serve(version, file string, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.assets["/"+version+"/"+file] = data
}

// origin is the registry's key in auth.json: its host and port.
func (m *mirror) origin() string {
	u, err := url.Parse(m.registry.URL)
	if err != nil {
		panic(err)
	}
	return u.Host
}

// served returns how many asset bodies the storage served, and whether it
// ever received credentials.
func (m *mirror) served() (downloads int, leaked bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.downloads, m.leaked
}
