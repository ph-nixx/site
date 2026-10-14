package repos

import (
	"errors"
	"regexp"

	"github.com/google/go-github/v91/github"
)

var (
	ErrDocsSyntax = errors.New("docs.toml syntax error") // log a warning and stop: the repo stays frozen
	ErrCompare    = errors.New("compare failed")         // a failure: nothing is written
)

// A table name or segment in docs.toml.
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// The project settings and node tree read from docs.toml, resolved against the cached repo and the comparison.
type DocsConfig struct {
	FullName string    // owner/name of the pushed repo, used for GitHub API requests
	SHA      string    // the commit docs.toml was read at, event.After
	Title    string    // [project].title, or the repo name with the first character capitalized
	SVG      string    // [project].svg
	ReadMe   []byte    // contents of README.md at SHA, fetched alongside docs.toml; nil when it returned 404
	Sections []DocNode // every table up to depth 3, in docs.toml order

	// The new, outdated, and unfrozen paths to fetch at SHA (every referenced path when the comparison cannot be trusted),
	// leaving out Skipped subtrees.
	UpdatePaths []string
}

// A node of the docs.toml tree as ingestion sees it; buildRepo turns the kept ones into DocSections.
type DocNode struct {
	Key        string    // table key path, e.g. "install.macos"; the node's identity across pushes
	Segment    string    // from docs.toml, or the cached segment while Frozen
	Title      string    // from docs.toml, or the cached title while Frozen
	Path       string    // from docs.toml, as written
	CachedPath string    // the cached counterpart's path, empty when never ingested
	Frozen     bool      // invalid with a cached counterpart: keeps its cached fields, its children are reconciled normally
	Skipped    bool      // invalid with no cached counterpart: left out with its whole subtree
	Missing    bool      // Path is known not to exist: removed in the comparison or 404 on fetch
	Children   []DocNode // in docs.toml order
}

// Parses docs.toml in table order, resolves it against cached, and marks it from the comparison: nodes with a parse time
// error are Frozen when cached and Skipped with their subtree when not, unchanged paths the comparison reports removed
// are Missing, and UpdatePaths lists the paths to fetch.
func reconcileDocsConfig(data []byte, event *github.PushEvent, cached *Repo, comp *github.CommitsComparison, compErr error) (*DocsConfig, error) {
	panic("unimplemented")
}
