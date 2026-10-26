//go:build integration

package repos

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"testing"
)

// [Repos.Doc] against a real Redis: resolves a page URL through the name map and DocRoutes the local cache derived
// from the stored repo, reads the HTML from {Repo.ID}:docs, and follows renames and deep cleans published on
// repos:updates.
func Test_doc_fetches_cached_html_from_redis(t *testing.T) {
	rdb := startRedis(t)
	const id = 7
	key, docsKey := strconv.Itoa(id), strconv.Itoa(id)+DOCS_KEY_SUFFIX

	// writes repo to the repos hash as a commit would; the caller publishes the update when the cache must see it
	store := func(t *testing.T, repo Repo) {
		t.Helper()
		data, err := json.Marshal(repo)
		if err != nil {
			t.Fatal(err)
		}
		if err := rdb.HSet(t.Context(), REPOS_KEY, key, data).Err(); err != nil {
			t.Fatal(err)
		}
	}

	repo := Repo{ID: id, Name: "my-lib", DocSections: []DocSection{
		{Key: "getting-started", Segment: "getting-started", Path: "docs/getting-started.md"},
		{Key: "install", Segment: "setup", SubSections: []DocSection{ // heading only, with its segment overridden
			{Key: "install.linux", Segment: "linux", Path: "docs/install/linux.md"},
			{Key: "install.macos", Segment: "macos", Path: "docs/install/macos.md", Frozen: true},
		}},
		{Key: "guides", Segment: "guides", SubSections: []DocSection{
			{Key: "guides.database", Segment: "database", Path: "docs/guides/database.md", SubSections: []DocSection{
				{Key: "guides.database.pooling", Segment: "pooling", Path: "docs/guides/database-pooling.md"},
			}},
		}},
		{Key: "uncached", Segment: "uncached", Path: "docs/uncached.md"}, // routed, but its HTML was never written
	}}
	store(t, repo)
	if err := rdb.HSet(t.Context(), docsKey, map[string]string{
		README_PATH:                       "<h1>my-lib</h1>",
		"docs/getting-started.md":         "<p>getting started</p>",
		"docs/install/linux.md":           "<p>linux</p>",
		"docs/install/macos.md":           "<p>macos, frozen</p>",
		"docs/guides/database-pooling.md": "<p>pooling</p>",
	}).Err(); err != nil {
		t.Fatal(err)
	}
	// another repo's docs, which a lookup by this repo's name must never read
	if err := rdb.HSet(t.Context(), "8"+DOCS_KEY_SUFFIX, README_PATH, "<h1>other</h1>").Err(); err != nil {
		t.Fatal(err)
	}

	r, err := New(t.Context(), FromRedisClient(rdb), nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		url  string
		html string
		err  error
	}{
		{"project page", "/my-lib", "<h1>my-lib</h1>", nil},
		{"project page with a trailing slash", "/my-lib/", "<h1>my-lib</h1>", nil},
		{"chapter", "/my-lib/getting-started", "<p>getting started</p>", nil},
		{"section under an overridden segment", "/my-lib/setup/linux", "<p>linux</p>", nil},
		{"frozen section serves its cached path", "/my-lib/setup/macos", "<p>macos, frozen</p>", nil},
		{"subsection three levels deep", "/my-lib/guides/database/pooling", "<p>pooling</p>", nil},
		{"section with a trailing slash", "/my-lib/setup/linux/", "<p>linux</p>", nil},
		{"table name instead of its segment", "/my-lib/install/linux", "", ErrUnknownPath},
		{"heading only chapter", "/my-lib/setup", "", ErrUnknownPath},
		{"file path instead of a route", "/my-lib/docs/install/linux.md", "", ErrUnknownPath},
		{"too many segments", "/my-lib/guides/database/pooling/tuning", "", ErrUnknownPath},
		{"routed page whose html is missing", "/my-lib/uncached", "", ErrUnknownPath},
		{"unknown repo", "/other", "", ErrUnknownRepo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			html, err := r.Doc(t.Context(), tt.url)
			if !errors.Is(err, tt.err) {
				t.Fatal("expected", tt.err, "got", err)
			}
			if html != tt.html {
				t.Errorf("expected %q got %q", tt.html, html)
			}
		})
	}

	// these mutate the stored repo, so they run in order after the lookups above
	t.Run("a rename moves the repo to its new name", func(t *testing.T) {
		renamed := repo
		renamed.Name = "my-lib-v2"
		store(t, renamed)
		if err := rdb.Publish(t.Context(), REPOS_UPDATES_CHANNEL, REPOS_UPDATE_PREFIX+":"+key).Err(); err != nil {
			t.Fatal(err)
		}
		eventually(t, func() bool {
			html, err := r.Doc(t.Context(), "/my-lib-v2/setup/linux")
			return err == nil && html == "<p>linux</p>"
		}, "the new name to resolve")
		if _, err := r.Doc(t.Context(), "/my-lib/setup/linux"); !errors.Is(err, ErrUnknownRepo) {
			t.Error("expected the old name to stop resolving, got", err)
		}
	})

	t.Run("a deep clean removes the repo", func(t *testing.T) {
		if err := rdb.HDel(t.Context(), REPOS_KEY, key).Err(); err != nil {
			t.Fatal(err)
		}
		if err := rdb.Del(t.Context(), docsKey).Err(); err != nil {
			t.Fatal(err)
		}
		if err := rdb.Publish(t.Context(), REPOS_UPDATES_CHANNEL, REPOS_UPDATE_PREFIX+":"+key).Err(); err != nil {
			t.Fatal(err)
		}
		eventually(t, func() bool {
			_, err := r.Doc(t.Context(), "/my-lib-v2")
			return errors.Is(err, ErrUnknownRepo)
		}, "the deep cleaned repo to leave the local cache")
	})
}
