package repos

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v91/github"
)

// mockGithub is a Github with canned files; Compare reports how its context ended on compared.
type mockGithub struct {
	files         map[string]File
	fetchErr      error
	compareBlocks bool       // Compare waits for its context to be canceled instead of returning at once
	compared      chan error // receives ctx.Err() when Compare returns
	status        string     // comparison status Compare returns, "ahead" when empty
}

func newMockGithub(files map[string]File, fetchErr error, compareBlocks bool) *mockGithub {
	return &mockGithub{files: files, fetchErr: fetchErr, compareBlocks: compareBlocks, compared: make(chan error, 1)}
}

func (g *mockGithub) FetchFiles(ctx context.Context, owner, repo, sha string, paths ...string) (map[string]File, error) {
	return g.files, g.fetchErr
}

func (g *mockGithub) Compare(ctx context.Context, owner, repo, base, head string) (*github.CommitsComparison, error) {
	if g.compareBlocks {
		<-ctx.Done()
	}
	g.compared <- ctx.Err()
	return &github.CommitsComparison{Status: github.Ptr(cmp.Or(g.status, "ahead"))}, ctx.Err()
}

// Returned in place of the panic of a step that is not implemented yet.
var errUnimplemented = errors.New("reached unimplemented code")

// [Repos.Set] returns without waiting for Compare on every stop that does not depend on the comparison,
// canceling it, and waits for Compare on the cases process zero must classify first.
func Test_set_stops_without_waiting_for_compare_when_the_files_alone_decide(t *testing.T) {
	found := File{Status: FileFound, Data: []byte("x")} // not TOML: a case past process zero stops at ErrDocsSyntax
	missing := File{Status: FileMissing}
	unreadable := File{Status: FileUnreadable}
	fetchErr := errors.New("boom")

	tests := []struct {
		name     string
		cached   bool // the repo is cached with a LastIngestedSHA, so Compare runs
		docsToml File
		readMe   File
		fetchErr error
		stops    bool  // Set returns without waiting for Compare
		err      error // what Set returns
	}{
		{"fetch error", true, found, found, fetchErr, true, fetchErr},
		{"docs.toml unreadable", true, unreadable, found, nil, true, nil},
		{"docs.toml unreadable on first ingest", false, unreadable, found, nil, true, nil},
		{"docs.toml missing on first ingest", false, missing, found, nil, true, nil},
		{"README.md missing on first ingest", false, found, missing, nil, true, nil},
		{"README.md unreadable on first ingest", false, found, unreadable, nil, true, nil},
		{"README.md missing once cached", true, found, missing, nil, false, nil},
		{"docs.toml missing once cached", true, missing, found, nil, false, errUnimplemented},
		{"both files found on first ingest", false, found, found, nil, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gh := newMockGithub(map[string]File{"docs.toml": tt.docsToml, "README.md": tt.readMe}, tt.fetchErr, tt.stops)
			r := Repos{client: newMockClient(map[string]string{}), github: gh, repoCache: RepoCache{}, logger: discard}
			if tt.cached {
				r.repoCache[1] = &Repo{ID: 1, LastIngestedSHA: "a"}
			}
			event := &github.PushEvent{
				After: github.Ptr("b"),
				Repo: &github.PushEventRepository{
					ID:    github.Ptr(int64(1)),
					Name:  github.Ptr("one"),
					Owner: &github.User{Login: github.Ptr("owner")},
				},
			}

			done := make(chan error, 1)
			go func() {
				// a deep clean reaches the mock client's unimplemented Watch
				defer func() {
					if p := recover(); p != nil {
						if p != "unimplemented" {
							panic(p)
						}
						done <- errUnimplemented
					}
				}()
				done <- r.Set(t.Context(), event)
			}()
			var err error
			select {
			case err = <-done:
			case <-time.After(time.Second):
				t.Fatal("Set waited for a Compare it should have canceled")
			}
			if !errors.Is(err, tt.err) {
				t.Error("expected", tt.err, "got", err)
			}

			if !tt.cached {
				select {
				case <-gh.compared:
					t.Error("expected no Compare on a first ingest")
				default:
				}
				return
			}
			select {
			case compareErr := <-gh.compared:
				if canceled := errors.Is(compareErr, context.Canceled); canceled != tt.stops {
					t.Error("expected Compare canceled", tt.stops, "got", canceled)
				}
			case <-time.After(time.Second):
				t.Fatal("Compare never returned")
			}
		})
	}
}

// [Repos.Set] classifies a diverged comparison by LastPushedAt instead of failing on its status.
func Test_set_does_not_fail_on_a_diverged_comparison(t *testing.T) {
	found := File{Status: FileFound, Data: []byte("x")}
	gh := newMockGithub(map[string]File{"docs.toml": found, "README.md": found}, nil, false)
	gh.status = "diverged"
	r := Repos{github: gh, repoCache: RepoCache{1: {ID: 1, LastIngestedSHA: "a"}}, logger: discard}
	event := &github.PushEvent{
		After: github.Ptr("b"),
		Repo: &github.PushEventRepository{
			ID:    github.Ptr(int64(1)),
			Name:  github.Ptr("one"),
			Owner: &github.User{Login: github.Ptr("owner")},
		},
	}

	if err := r.Set(t.Context(), event); err != nil {
		t.Error("expected no error", "got", err)
	}
}

// [Repos.fetchDocs] marks Missing exactly the nodes, at any depth, whose fetched path is missing or unreadable, so
// buildRepo freezes or skips the right ones, and renders only the files it found.
func Test_fetch_docs_marks_missing_the_nodes_whose_files_were_not_found(t *testing.T) {
	gh := newMockGithub(map[string]File{
		"docs/getting-started.md":         {Status: FileFound, Data: []byte("# Getting started")},
		"docs/install/linux.md":           {Status: FileMissing},
		"docs/install/macos.md":           {Status: FileUnreadable},
		"docs/guides/database-pooling.md": {Status: FileMissing},
	}, nil, false)
	r := Repos{github: gh, logger: discard}
	cfg := &DocsConfig{
		ReadMe: []byte("# One"),
		Sections: []DocNode{
			{Key: "getting-started", Path: "docs/getting-started.md"},
			{Key: "install", Children: []DocNode{ // heading only: no path, never Missing
				{Key: "install.linux", Path: "docs/install/linux.md"},                                                     // new, would be skipped
				{Key: "install.macos", Path: "docs/install/macos.md", Cached: &DocSection{Path: "docs/install/macos.md"}}, // cached, would be frozen
			}},
			{Key: "guides", Children: []DocNode{
				{Key: "guides.database", Path: "docs/guides/database.md", Children: []DocNode{ // unchanged, not fetched
					{Key: "guides.database.pooling", Path: "docs/guides/database-pooling.md"}, // depth 3
				}},
				{Key: "guides.caching", Path: "docs/guides/caching.md", Missing: true}, // removed in the comparison
			}},
		},
		UpdatePaths: []string{"docs/getting-started.md", "docs/install/linux.md", "docs/install/macos.md", "docs/guides/database-pooling.md"},
	}

	html, err := r.fetchDocs(t.Context(), cfg)
	if err != nil {
		t.Fatal("expected no error", "got", err)
	}

	got := map[string]bool{}
	var walk func(nodes []DocNode)
	walk = func(nodes []DocNode) {
		for _, n := range nodes {
			got[n.Key] = n.Missing
			walk(n.Children)
		}
	}
	walk(cfg.Sections)
	want := map[string]bool{
		"getting-started":         false,
		"install":                 false,
		"install.linux":           true,
		"install.macos":           true,
		"guides":                  false,
		"guides.database":         false,
		"guides.database.pooling": true,
		"guides.caching":          true,
	}
	if !maps.Equal(got, want) {
		t.Error("expected Missing", want, "got", got)
	}

	if paths, want := slices.Sorted(maps.Keys(html)), []string{"README.md", "docs/getting-started.md"}; !slices.Equal(paths, want) {
		t.Error("expected html for", want, "got", paths)
	}

	// every node fetchDocs marks Missing is warned about, frozen when cached and skipped when not
	warned := map[string]string{}
	for _, w := range cfg.Warnings {
		warned[w.Key] = w.Reason
	}
	wantWarned := map[string]string{
		"install.linux":           "its file is missing or unreadable at the pushed commit; skipped with its subtree, since it was never ingested",
		"install.macos":           "its file is missing or unreadable at the pushed commit; frozen at its cached version",
		"guides.database.pooling": "its file is missing or unreadable at the pushed commit; skipped with its subtree, since it was never ingested",
	}
	if !maps.Equal(warned, wantWarned) {
		t.Error("expected warnings", wantWarned, "got", warned)
	}
}

// [Repos.Set] logs a warning, tagged with the repo and the pushed SHA, for every stop that freezes the repo or leaves it
// out, and for every invalid docs.toml node.
func Test_set_logs_why_it_stops(t *testing.T) {
	found := func(data string) File { return File{Status: FileFound, Data: []byte(data)} }
	tests := []struct {
		name     string
		docsToml File
		readMe   File
		logged   string
	}{
		{"unreadable docs.toml", File{Status: FileUnreadable}, found("# x"), "docs.toml is binary or too large"},
		{"README.md missing on the first ingest", found(""), File{Status: FileMissing}, "README.md is missing or unreadable on the first ingest"},
		{"docs.toml syntax error", found("[guides"), found("# x"), "docs.toml does not parse"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			gh := newMockGithub(map[string]File{DOCS_CONFIG_PATH: tt.docsToml, README_PATH: tt.readMe}, nil, false)
			r := Repos{github: gh, repoCache: RepoCache{}, logger: slog.New(slog.NewTextHandler(&buf, nil))}
			event := &github.PushEvent{After: github.Ptr("b"), Repo: &github.PushEventRepository{
				ID: github.Ptr(int64(1)), Name: github.Ptr("one"), FullName: github.Ptr("owner/one"),
				Owner: &github.User{Login: github.Ptr("owner")},
			}}

			if err := r.Set(t.Context(), event); err != nil {
				t.Fatal("expected no error, got", err)
			}
			out := buf.String()
			for _, want := range []string{"level=WARN", tt.logged, "repo=owner/one", "after=b"} {
				if !strings.Contains(out, want) {
					t.Errorf("expected the log to contain %q, got %q", want, out)
				}
			}
		})
	}
}

// [buildRepo] keeps the kept nodes in docs.toml order at every depth, freezes the invalid ones that have a cached
// counterpart with its cached fields, and leaves out skipped ones with their whole subtree.
func Test_build_repo_builds_sections_in_docs_toml_order(t *testing.T) {
	cfg := &DocsConfig{Sections: []DocNode{
		{Key: "getting-started", Segment: "getting-started", Title: "Getting started", Path: "docs/getting-started.md"},
		{Key: "install", Segment: "install", Title: "Installation", Children: []DocNode{
			{Key: "install.windows", Segment: "windows", Title: "Windows", Path: "docs/install/windows.md", Missing: true, Children: []DocNode{
				{Key: "install.windows.wsl", Segment: "wsl", Title: "Wsl", Path: "docs/install/wsl.md"}, // left out with its parent
			}},
			{Key: "install.linux", Segment: "linux", Title: "Linux", Path: "docs/install/linux.md"},
			{Key: "install.macos", Segment: "mac", Title: "Mac", Path: "docs/install/mac.md", Missing: true,
				Cached: &DocSection{Key: "install.macos", Segment: "macos", Title: "macOS", Path: "docs/install/macos.md"},
				Children: []DocNode{
					{Key: "install.macos.arm", Segment: "arm", Title: "Arm", Path: "docs/install/arm.md"}, // kept under its frozen parent
				}},
		}},
		{Key: "Bad", Segment: "Bad", Title: "Bad", Path: "docs/bad.md", Skipped: true},
		{Key: "guides", Segment: "guides", Title: "Guides", Children: []DocNode{
			{Key: "guides.database", Segment: "db", Title: "Database", Path: "docs/guides/database.md", Frozen: true,
				Cached: &DocSection{Key: "guides.database", Segment: "database", Title: "Database", Path: "docs/guides/db.md"}},
			{Key: "guides.caching", Segment: "caching", Title: "Caching", Path: "docs/guides/caching.md"},
		}},
	}}
	event := &github.PushEvent{After: github.Ptr("b"), Repo: &github.PushEventRepository{ID: github.Ptr(int64(1))}}

	got := buildRepo(event, cfg).DocSections
	want := []DocSection{
		{Key: "getting-started", Segment: "getting-started", Title: "Getting started", Path: "docs/getting-started.md"},
		{Key: "install", Segment: "install", Title: "Installation", SubSections: []DocSection{
			{Key: "install.linux", Segment: "linux", Title: "Linux", Path: "docs/install/linux.md"},
			{Key: "install.macos", Frozen: true, Segment: "macos", Title: "macOS", Path: "docs/install/macos.md", SubSections: []DocSection{
				{Key: "install.macos.arm", Segment: "arm", Title: "Arm", Path: "docs/install/arm.md"},
			}},
		}},
		{Key: "guides", Segment: "guides", Title: "Guides", SubSections: []DocSection{
			{Key: "guides.database", Frozen: true, Segment: "database", Title: "Database", Path: "docs/guides/db.md"},
			{Key: "guides.caching", Segment: "caching", Title: "Caching", Path: "docs/guides/caching.md"},
		}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("expected %+v got %+v", want, got)
	}
}

// [pruneRepo] lists the paths the stored tree references and the new one does not, in any order, and never README.md.
func Test_prune_repo_lists_the_paths_no_longer_referenced(t *testing.T) {
	linux := DocSection{Key: "install.linux", Segment: "linux", Path: "docs/install/linux.md"}
	macos := DocSection{Key: "install.macos", Segment: "macos", Path: "docs/install/macos.md"}
	install := DocSection{Key: "install", Segment: "install", SubSections: []DocSection{linux, macos}}
	pooling := DocSection{Key: "guides.database.pooling", Segment: "pooling", Path: "docs/guides/database-pooling.md"}
	database := DocSection{Key: "guides.database", Segment: "database", Path: "docs/guides/database.md", SubSections: []DocSection{pooling}}
	guides := DocSection{Key: "guides", Segment: "guides", SubSections: []DocSection{database}}
	stored := &Repo{DocSections: []DocSection{install, guides}}

	movedMacos := macos
	movedMacos.Path = "docs/install/mac.md"
	frozenMacos := macos
	frozenMacos.Frozen = true
	readMe := DocSection{Key: "readme", Segment: "readme", Path: README_PATH}

	tests := []struct {
		name          string
		before, after *Repo
		old           []string
	}{
		{"first ingest", nil, stored, nil},
		{"unchanged tree", stored, stored, nil},
		{"reordered tables", stored, &Repo{DocSections: []DocSection{guides, install}}, nil},
		{"removed table takes its descendants", stored, &Repo{DocSections: []DocSection{install}},
			[]string{"docs/guides/database.md", "docs/guides/database-pooling.md"}},
		{"removed heading only chapter takes its sections", stored, &Repo{DocSections: []DocSection{guides}},
			[]string{"docs/install/linux.md", "docs/install/macos.md"}},
		{"node points to a new file", stored, &Repo{DocSections: []DocSection{
			{Key: "install", Segment: "install", SubSections: []DocSection{linux, movedMacos}}, guides}},
			[]string{"docs/install/macos.md"}},
		{"frozen node keeps its cached file", stored, &Repo{DocSections: []DocSection{
			{Key: "install", Segment: "install", SubSections: []DocSection{linux, frozenMacos}}, guides}}, nil},
		{"file still referenced by another node", stored, &Repo{DocSections: []DocSection{
			{Key: "install", Segment: "install", SubSections: []DocSection{linux}},
			{Key: "apple", Segment: "apple", Path: "docs/install/macos.md"}, guides}}, nil},
		{"README.md is never old", &Repo{DocSections: []DocSection{readMe, install}}, &Repo{DocSections: []DocSection{install}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pruneRepo(tt.before, tt.after)
			// old comes in no particular order, so it is compared as a set
			set := func(paths []string) map[string]bool {
				m := map[string]bool{}
				for _, p := range paths {
					m[p] = true
				}
				return m
			}
			if len(got) != len(tt.old) || !maps.Equal(set(got), set(tt.old)) {
				t.Error("expected", tt.old, "got", got)
			}
		})
	}
}

func Test_into_html_wraps_tables_for_scrolling(t *testing.T) {
	got := intoHTML([]byte("| id | result |\n|---|---|\n| 1 | B+ |\n\nafter\n"))
	want := "<div class=\"table-scroll\">\n<table>\n<thead>\n<tr>\n<th>id</th>\n<th>result</th>\n</tr>\n</thead>\n" +
		"<tbody>\n<tr>\n<td>1</td>\n<td>B+</td>\n</tr>\n</tbody>\n</table>\n</div>\n<p>after</p>\n"
	if got != want {
		t.Errorf("intoHTML() = %q, want %q", got, want)
	}
}
