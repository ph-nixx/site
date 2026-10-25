package repos

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
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
	ReadMe   []byte    // contents of README.md at SHA, fetched alongside docs.toml; nil when missing, unreadable, or unchanged
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
	Key      string      // table key path, e.g. "install.macos"; the node's identity across pushes
	Segment  string      // from docs.toml, or the cached segment while Frozen
	Title    string      // from docs.toml, or the cached title while Frozen
	Path     string      // from docs.toml, as written
	Cached   *DocSection // the cached node with the same Key, nil when never ingested
	Frozen   bool        // invalid with a cached counterpart: keeps its cached fields, its children are reconciled normally
	Skipped  bool        // invalid with no cached counterpart: left out with its whole subtree
	Missing  bool        // Path is known not to exist or not to be text: removed in the comparison, or missing or unreadable on fetch
	Children []DocNode   // in docs.toml order
}

// Parses docs.toml in table order, resolves it against cached, and marks it from the comparison: nodes with a parse time
// error are Frozen when cached and Skipped with their subtree when not, unchanged paths the comparison reports removed
// are Missing, and UpdatePaths lists the paths to fetch.
// ReadMe holds readMe's contents only when README.md is found and new or outdated: the first ingest, a refetch all, or
// listed as added, modified, or renamed in the comparison; otherwise it stays nil and the cached project page is kept.
// It first orders S and After from the comparison, returning ErrStale before anything can act on a stale After; a
// missing docs.toml then returns ErrDocsRemoved, or ErrCompare when a 5xx leaves the deep clean unsafe.
// compErr is nil, a 404, or a 5xx: Set has already failed on every other comparison error.
func reconcileDocsConfig(docsToml, readMe File, event *github.PushEvent, cached *Repo, comp *github.CommitsComparison, compErr error) (*DocsConfig, error) {
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

	var doc map[string]any
	md, err := toml.Decode(string(docsToml.Data), &doc)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDocsSyntax, err)
	}

	// only an ahead comparison lists what changed since S; anything else refetches every referenced path
	usable := compErr == nil && comp.GetStatus() == "ahead"
	refetchAll := !usable || len(comp.Files) >= COMPARE_FILE_LIMIT
	changed, removed := map[string]bool{}, map[string]bool{}
	if !refetchAll {
		for _, f := range comp.Files {
			switch f.GetStatus() {
			case "removed":
				removed[f.GetFilename()] = true
			case "renamed":
				removed[f.GetPreviousFilename()] = true
				changed[f.GetFilename()] = true
			default:
				changed[f.GetFilename()] = true
			}
		}
	}

	// the cached tree by key, and every path it references (before)
	byKey, before := map[string]*DocSection{}, map[string]bool{}
	if cached != nil {
		for stack := [][]DocSection{cached.DocSections}; len(stack) > 0; {
			sections := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for i := range sections {
				byKey[sections[i].Key] = &sections[i]
				if sections[i].Path != "" {
					before[sections[i].Path] = true
				}
				stack = append(stack, sections[i].SubSections)
			}
		}
	}

	repo := event.GetRepo()
	cfg := &DocsConfig{
		Owner:       repo.GetOwner().GetLogin(),
		Name:        repo.GetName(),
		SHA:         event.GetAfter(),
		Title:       capitalize(repo.GetName()),
		Sections:    parseNodes(doc, tableKeys(md), nil, byKey),
		PushCommits: !usable,
	}
	if project, ok := doc["project"].(map[string]any); ok {
		if title, ok := project["title"].(string); ok && title != "" {
			cfg.Title = title
		}
		if svg, ok := project["svg"].(string); ok {
			cfg.SVG = svg
		}
	}
	if readMe.Status == FileFound && (refetchAll || changed[README_PATH]) {
		cfg.ReadMe = readMe.Data
	}

	// new, outdated, and unfrozen paths are fetched; an unchanged path the comparison removed is Missing
	seen := map[string]bool{}
	for stack := [][]DocNode{cfg.Sections}; len(stack) > 0; {
		nodes := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for i := range nodes {
			n := &nodes[i]
			if n.Skipped {
				continue // left out with its whole subtree
			}
			stack = append(stack, n.Children)
			if n.Frozen || n.Path == "" {
				continue
			}
			switch {
			case refetchAll, !before[n.Path], changed[n.Path], n.Cached != nil && n.Cached.Frozen:
				if !seen[n.Path] {
					seen[n.Path] = true
					cfg.UpdatePaths = append(cfg.UpdatePaths, n.Path)
				}
			case removed[n.Path]:
				n.Missing = true
			}
		}
	}
	return cfg, nil
}

// Lists every table key path in docs.toml in the order each first appears, including the implicit tables that only
// a deeper header (e.g. [guides] for [guides.database]) or a dotted key defines.
func tableKeys(md toml.MetaData) [][]string {
	seen := map[string]bool{}
	var keys [][]string
	for _, k := range md.Keys() {
		n := len(k) - 1 // a value's parents are tables
		if md.Type(k...) == "Hash" {
			n = len(k)
		}
		for i := 1; i <= n; i++ {
			if p := strings.Join(k[:i], "."); !seen[p] {
				seen[p] = true
				keys = append(keys, k[:i])
			}
		}
	}
	return keys
}

// Parses the tables directly under prefix into nodes in docs.toml order, then resolves each against its cached
// counterpart in cached: an invalid node is Frozen with its cached segment and title when it has one, and Skipped
// when it does not.
func parseNodes(doc map[string]any, keys [][]string, prefix []string, cached map[string]*DocSection) []DocNode {
	var (
		nodes []DocNode
		valid []bool
	)
	for _, k := range keys {
		if len(k) != len(prefix)+1 || !slices.Equal(k[:len(prefix)], prefix) || slices.Equal(k, []string{"project"}) {
			continue
		}
		table, ok := lookup(doc, k)
		if !ok {
			continue // an array of tables, not a node
		}
		n, ok := parseNode(k, table)
		if len(k) < MAX_DOC_DEPTH {
			n.Children = parseNodes(doc, keys, k, cached)
		} // TODO: log warning for tables deeper than MAX_DOC_DEPTH, which are ignored
		nodes, valid = append(nodes, n), append(valid, ok)
	}

	// siblings that resolve to the same segment are all invalid
	segments := map[string]int{}
	for _, n := range nodes {
		segments[n.Segment]++
	}
	for i := range nodes {
		n := &nodes[i]
		n.Cached = cached[n.Key]
		switch {
		case valid[i] && segments[n.Segment] == 1:
		case n.Cached != nil:
			n.Frozen, n.Segment, n.Title = true, n.Cached.Segment, n.Cached.Title // TODO: log warning
		default:
			n.Skipped = true // TODO: log warning
		}
	}
	return nodes
}

// Reads one table's segment, title, and path, defaulting segment and title from its table name; ok is false on a
// node level error: a name or segment that is not a slug, a value that is not a string, a section without a path, or
// a path that does not end in .md.
func parseNode(key []string, table map[string]any) (n DocNode, ok bool) {
	name := key[len(key)-1]
	ok = slugPattern.MatchString(name)
	field := func(name, fallback string) string {
		v, set := table[name]
		if !set {
			return fallback
		}
		s, isString := v.(string)
		ok = ok && isString
		return s
	}
	n = DocNode{
		Key:     strings.Join(key, "."),
		Segment: field("segment", name),
		Title:   field("title", capitalize(strings.ReplaceAll(name, "-", " "))),
		Path:    field("path", ""),
	}
	ok = ok && slugPattern.MatchString(n.Segment) &&
		(n.Path != "" || len(key) == 1) && (n.Path == "" || strings.HasSuffix(n.Path, ".md"))
	return n, ok
}

// Walks doc down key to the table it names; ok is false when any step is not a table.
func lookup(doc map[string]any, key []string) (table map[string]any, ok bool) {
	table = doc
	for _, name := range key {
		if table, ok = table[name].(map[string]any); !ok {
			return nil, false
		}
	}
	return table, true
}

// Upper cases the first character of s.
func capitalize(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}
