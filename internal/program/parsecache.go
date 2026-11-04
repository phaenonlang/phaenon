package program

import (
	"bytes"
	"sync"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
)

// ParseCache keeps parsed files between programs, for a process that builds
// many programs of one project: phn lsp builds one for every edit. A file
// whose path, mode and text are the same as last time is not parsed again,
// and the programs share its tree. That is safe, also across goroutines,
// because nothing writes into a tree after the parse: the declarations,
// types and findings are the program's and the checker's own objects.
type ParseCache struct {
	mu    sync.Mutex
	files map[string]cachedParse
}

type cachedParse struct {
	src  []byte
	mode lexer.Mode
	tree *ast.File
}

// NewParseCache returns an empty cache.
func NewParseCache() *ParseCache { return &ParseCache{files: map[string]cachedParse{}} }

// parse returns the tree of src, from the cache when it holds the same
// text; a nil cache parses every time. A file that fails to parse is not
// kept.
func (c *ParseCache) parse(path string, src []byte, mode lexer.Mode) (*ast.File, error) {
	if c == nil {
		return parser.Parse(src, mode)
	}
	c.mu.Lock()
	hit, ok := c.files[path]
	c.mu.Unlock()
	if ok && hit.mode == mode && bytes.Equal(hit.src, src) {
		return hit.tree, nil
	}
	tree, err := parser.Parse(src, mode)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.files[path] = cachedParse{src: src, mode: mode, tree: tree}
	c.mu.Unlock()
	return tree, nil
}
