package repos

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"

	"github.com/google/go-github/v91/github"
)

var (
	ErrDocsSyntax  = errors.New("docs.toml syntax error") // log a warning and stop: the repo stays frozen
	ErrCompare     = errors.New("compare failed")         // a failure: nothing is written
	ErrStale       = errors.New("push already ingested")  // stop: a push at or past After was already ingested
	ErrDocsRemoved = errors.New("docs.toml removed")      // deep clean: docs.toml is gone at a push that is not stale
)

// A table name or segment in docs.toml.
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// The project settings and node tree read from docs.toml, resolved against the cached repo and the comparison.
type DocsConfig struct {
	Owner    string    // the pushed repo's owner login, event.Repo.Owner.Login, used for GitHub API requests
	Name     string    // the pushed repo's name, event.Repo.Name, used for GitHub API requests
	SHA      string    // the commit docs.toml was read at, event.After
	Title    string    // [project].title, or the repo name with the first character capitalized
	SVG      string    // [project].svg
	ReadMe   []byte    // contents of README.md at SHA, fetched alongside docs.toml; nil when missing or unreadable
	Sections []DocNode // every table up to depth 3, in docs.toml order

	// The new, outdated, and unfrozen paths to fetch at SHA (every referenced path when the comparison cannot be trusted),
	// leaving out Skipped subtrees.
	UpdatePaths []string

	// Process two reads event.Commits instead of the comparison's commits: S is empty, or the comparison diverged or
	// failed with a 404 or 5xx; only an ahead comparison lists the commits since S.
	PushCommits bool
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
	Missing    bool      // Path is known not to exist or not to be text: removed in the comparison, or missing or unreadable on fetch
	Children   []DocNode // in docs.toml order
}

// Parses docs.toml in table order, resolves it against cached, and marks it from the comparison: nodes with a parse time
// error are Frozen when cached and Skipped with their subtree when not, unchanged paths the comparison reports removed
// are Missing, and UpdatePaths lists the paths to fetch.
// It first orders S and After from the comparison, returning ErrStale before anything can act on a stale After; a
// missing docs.toml then returns ErrDocsRemoved, or ErrCompare when a 5xx leaves the deep clean unsafe.
// compErr is nil, a 404, or a 5xx: Set has already failed on every other comparison error.
func reconcileDocsConfig(docsToml File, event *github.PushEvent, cached *Repo, comp *github.CommitsComparison, compErr error) (*DocsConfig, error) {
	if comp != nil || compErr != nil {
		switch status := comp.GetStatus(); {
		case compErr != nil, status == "diverged":
			if !cached.LastPushedAt.Before(event.GetRepo().GetPushedAt().Time) {
				return nil, ErrStale // a push GitHub received no earlier was already ingested
			}
		case status == "behind", status == "identical":
			return nil, ErrStale // a push at or past After was already ingested
		case status != "ahead":
			return nil, fmt.Errorf("unexpected comparison status %q", status)
		}
	}

	if docsToml.Status == FileMissing {
		var ghErr *github.ErrorResponse
		if errors.As(compErr, &ghErr) && ghErr.Response.StatusCode >= http.StatusInternalServerError {
			return nil, ErrCompare
		}
		return nil, ErrDocsRemoved
	}

	// TODO: set PushCommits to comp.GetStatus() != "ahead" in the returned config
	panic("unimplemented")
}
