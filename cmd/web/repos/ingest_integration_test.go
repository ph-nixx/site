//go:build integration

package repos

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v91/github"
	"github.com/redis/go-redis/v9"
)

// The Redis image startRedis runs; pinned so a new release cannot change test behavior.
const REDIS_TEST_IMAGE = "redis:7.4-alpine"

// Starts a throwaway Redis container on a free host port and returns a client connected to it; the container is
// removed when the test ends. Skips the test when docker is not installed.
func startRedis(t *testing.T) *redis.Client {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	docker := func(args ...string) string {
		out, err := exec.Command("docker", args...).Output()
		if exitErr := (*exec.ExitError)(nil); errors.As(err, &exitErr) {
			t.Fatalf("docker %s: %s", args[0], exitErr.Stderr)
		} else if err != nil {
			t.Fatal("docker", args[0], err)
		}
		return strings.TrimSpace(string(out))
	}

	// an empty host port lets docker pick a free one, so parallel runs never collide
	id := docker("run", "--detach", "--rm", "--publish", "127.0.0.1::6379", REDIS_TEST_IMAGE)
	t.Cleanup(func() { exec.Command("docker", "rm", "--force", id).Run() })
	addr, _, _ := strings.Cut(docker("port", id, "6379/tcp"), "\n")

	rdb := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { rdb.Close() })
	// the server accepts connections a moment after the container starts
	for deadline := time.Now().Add(10 * time.Second); rdb.Ping(t.Context()).Err() != nil; {
		if time.Now().After(deadline) {
			t.Fatal("redis did not accept connections within 10s")
		}
		time.Sleep(100 * time.Millisecond)
	}
	return rdb
}

// interferingClient writes to the watched repos hash from another connection inside the next aborts transactions,
// so their EXEC fails as if another repo had committed at the same time.
type interferingClient struct {
	Client
	rdb    *redis.Client
	aborts int
}

func (c *interferingClient) Watch(ctx context.Context, fn func(*redis.Tx) error, keys ...string) error {
	return c.Client.Watch(ctx, func(tx *redis.Tx) error {
		if c.aborts > 0 {
			c.aborts--
			c.rdb.HSet(ctx, REPOS_KEY, "other-repo", "{}")
		}
		return fn(tx)
	}, keys...)
}

// [Repos.commitDocs] against a real Redis: the SHA check, the ordering of pushes it cannot compare, the writes a
// successful commit applies together, docs and commits alike, and the retries after a WATCH abort.
func Test_commit_docs_against_redis(t *testing.T) {
	rdb := startRedis(t)
	const id = 7
	key, docsKey := strconv.Itoa(id), strconv.Itoa(id)+DOCS_KEY_SUFFIX
	pushed := time.Unix(1_000_000, 0).UTC()

	// commits of the repo: a1 is logged by the seeded commit, stale is past the 90 day retention
	recent := time.Now().Add(-time.Hour).Truncate(time.Second).UTC()
	a1 := ConventionalCommit{ID: "a1", RepoID: id, Timestamp: recent, Type: "feat"}
	a1CST := a1 // the same commit as the push event might report it, with a zone offset
	a1CST.Timestamp = a1.Timestamp.In(time.FixedZone("CST", -6*3600))
	c2 := ConventionalCommit{ID: "c2", RepoID: id, Timestamp: recent.Add(time.Minute), Type: "fix"}
	stale := ConventionalCommit{ID: "stale", RepoID: id, Timestamp: time.Now().Add(-COMMIT_RETENTION - time.Hour).UTC()}

	// writes repo, its docs, and a1 as a previous commit would have
	seed := func(t *testing.T, repo Repo, docs map[string]string) {
		t.Helper()
		data, err := json.Marshal(repo)
		if err != nil {
			t.Fatal(err)
		}
		if err := rdb.HSet(t.Context(), REPOS_KEY, key, data).Err(); err != nil {
			t.Fatal(err)
		}
		if len(docs) > 0 {
			if err := rdb.HSet(t.Context(), docsKey, docs).Err(); err != nil {
				t.Fatal(err)
			}
		}
		member := redis.Z{Score: float64(a1.Timestamp.Unix()), Member: commitJSON(t, a1)}
		if err := rdb.ZAdd(t.Context(), COMMITS_UPDATE_PREFIX, member).Err(); err != nil {
			t.Fatal(err)
		}
	}
	// the IDs of every commit in the log
	logged := func(t *testing.T) map[string]bool {
		t.Helper()
		ids := map[string]bool{}
		for _, m := range rdb.ZRange(t.Context(), COMMITS_UPDATE_PREFIX, 0, -1).Val() {
			var c ConventionalCommit
			if err := json.Unmarshal([]byte(m), &c); err != nil {
				t.Fatal(err)
			}
			ids[c.ID] = true
		}
		return ids
	}
	// the stored repo's LastIngestedSHA, empty when the field is missing
	storedSHA := func(t *testing.T) string {
		t.Helper()
		data, err := rdb.HGet(t.Context(), REPOS_KEY, key).Bytes()
		if errors.Is(err, redis.Nil) {
			return ""
		}
		var repo Repo
		if err != nil || json.Unmarshal(data, &repo) != nil {
			t.Fatal("reading the stored repo:", err)
		}
		return repo.LastIngestedSHA
	}
	// subscribes to the updates channel; the returned func reports every message published since, in order
	subscribe := func(t *testing.T) func() []string {
		t.Helper()
		sub := rdb.Subscribe(t.Context(), REPOS_UPDATES_CHANNEL)
		t.Cleanup(func() { sub.Close() })
		if _, err := sub.Receive(t.Context()); err != nil { // the subscription confirmation
			t.Fatal(err)
		}
		return func() []string {
			var payloads []string
			for {
				msg, err := sub.ReceiveTimeout(t.Context(), 200*time.Millisecond)
				m, ok := msg.(*redis.Message)
				if err != nil || !ok {
					return payloads
				}
				payloads = append(payloads, m.Payload)
			}
		}
	}

	stored := Repo{ID: id, LastIngestedSHA: "a", LastPushedAt: pushed, DocSections: []DocSection{
		{Key: "install", Segment: "install", SubSections: []DocSection{
			{Key: "install.linux", Segment: "linux", Path: "docs/install/linux.md"},
			{Key: "install.macos", Segment: "macos", Path: "docs/install/macos.md"},
		}},
		{Key: "guides", Segment: "guides", Path: "docs/guides.md"},
	}}
	storedDocs := map[string]string{
		README_PATH:             "<h1>readme</h1>",
		"docs/install/linux.md": "<p>linux</p>",
		"docs/install/macos.md": "<p>macos</p>",
		"docs/guides.md":        "<p>guides</p>",
	}
	// the next push: guides removed, linux edited, macos frozen at its cached path
	next := Repo{ID: id, LastIngestedSHA: "b", LastPushedAt: pushed.Add(time.Minute), DocSections: []DocSection{
		{Key: "install", Segment: "install", SubSections: []DocSection{
			{Key: "install.linux", Segment: "linux", Path: "docs/install/linux.md"},
			{Key: "install.macos", Segment: "macos", Path: "docs/install/macos.md", Frozen: true},
		}},
	}}

	tests := []struct {
		name     string
		seed     bool // the stored repo and its docs exist before the commit
		repo     Repo // the repo being committed
		expected string
		html     map[string]string
		commits  []ConventionalCommit
		aborts   int // WATCH aborts injected before the transaction can EXEC
		result   CommitResult
		fails    bool              // commitDocs returns an error
		docs     map[string]string // the docs hash afterwards
		sha      string            // the stored LastIngestedSHA afterwards
		logged   []string          // the IDs in the commit log afterwards
		reloads  bool              // a committed push also publishes commits, after repos:{ID}
	}{
		{
			name: "first ingest writes only the referenced html", repo: stored, expected: "", commits: []ConventionalCommit{c2},
			html:   map[string]string{README_PATH: "<h1>readme</h1>", "docs/install/linux.md": "<p>linux</p>", "docs/skipped.md": "<p>skipped</p>"},
			result: Committed, docs: map[string]string{README_PATH: "<h1>readme</h1>", "docs/install/linux.md": "<p>linux</p>"}, sha: "a",
			logged: []string{"c2"}, reloads: true,
		},
		{
			name: "commit prunes old paths and keeps frozen ones and README.md", seed: true, repo: next, expected: "a",
			html: map[string]string{"docs/install/linux.md": "<p>linux v2</p>"}, commits: []ConventionalCommit{a1, c2},
			result: Committed, sha: "b",
			docs:   map[string]string{README_PATH: "<h1>readme</h1>", "docs/install/linux.md": "<p>linux v2</p>", "docs/install/macos.md": "<p>macos</p>"},
			logged: []string{"a1", "c2"}, reloads: true,
		},
		{
			name: "metadata only change rewrites just the repo", seed: true, repo: Repo{ID: id, LastIngestedSHA: "b", LastPushedAt: pushed.Add(time.Minute), Title: "New", DocSections: stored.DocSections},
			expected: "a", commits: []ConventionalCommit{a1CST}, result: Committed, docs: storedDocs, sha: "b",
			logged: []string{"a1"}, // re-added with another zone offset: the same member, so no reload
		},
		{
			name: "an expired commit is added and pruned in the same commit", seed: true, repo: next, expected: "a",
			commits: []ConventionalCommit{stale}, result: Committed, sha: "b",
			docs:   map[string]string{README_PATH: "<h1>readme</h1>", "docs/install/linux.md": "<p>linux</p>", "docs/install/macos.md": "<p>macos</p>"},
			logged: []string{"a1"}, reloads: true,
		},
		{
			name: "a later push already committed: superseded", seed: true, repo: Repo{ID: id, LastIngestedSHA: "c", LastPushedAt: pushed.Add(-time.Minute)},
			expected: "z", commits: []ConventionalCommit{c2}, result: Superseded, docs: storedDocs, sha: "a", logged: []string{"a1"},
		},
		{
			name: "pushed at the same second: overtaken", seed: true, repo: Repo{ID: id, LastIngestedSHA: "c", LastPushedAt: pushed},
			expected: "z", commits: []ConventionalCommit{c2}, result: Overtaken, docs: storedDocs, sha: "a", logged: []string{"a1"},
		},
		{
			name: "an earlier push committed first: overtaken", seed: true, repo: Repo{ID: id, LastIngestedSHA: "c", LastPushedAt: pushed.Add(time.Minute)},
			expected: "z", commits: []ConventionalCommit{c2}, result: Overtaken, docs: storedDocs, sha: "a", logged: []string{"a1"},
		},
		{
			name: "a deep clean committed first: overtaken, and no commit outlives it", repo: next, expected: "a",
			html: map[string]string{"docs/install/linux.md": "<p>linux v2</p>"}, commits: []ConventionalCommit{c2},
			result: Overtaken, docs: map[string]string{}, sha: "",
		},
		{
			name: "aborted EXEC is retried until it commits", seed: true, repo: next, expected: "a", aborts: TX_RETRIES,
			commits: []ConventionalCommit{c2}, result: Committed, sha: "b",
			docs:   map[string]string{README_PATH: "<h1>readme</h1>", "docs/install/linux.md": "<p>linux</p>", "docs/install/macos.md": "<p>macos</p>"},
			logged: []string{"a1", "c2"}, reloads: true,
		},
		{
			name: "aborted EXEC past the retry cap fails", seed: true, repo: next, expected: "a", aborts: TX_RETRIES + 1,
			commits: []ConventionalCommit{c2}, fails: true, docs: storedDocs, sha: "a", logged: []string{"a1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := rdb.FlushDB(t.Context()).Err(); err != nil {
				t.Fatal(err)
			}
			if tt.seed {
				seed(t, stored, storedDocs)
			}
			published := subscribe(t)
			r := &Repos{client: &interferingClient{Client: FromRedisClient(rdb), rdb: rdb, aborts: tt.aborts}, logger: discard}

			result, err := r.commitDocs(t.Context(), tt.repo, tt.expected, tt.html, tt.commits)
			if tt.fails != (err != nil) {
				t.Fatal("expected failure", tt.fails, "got", err)
			}
			if !tt.fails && result != tt.result {
				t.Error("expected", tt.result, "got", result)
			}
			if docs := rdb.HGetAll(t.Context(), docsKey).Val(); !maps.Equal(docs, tt.docs) {
				t.Error("expected docs", tt.docs, "got", docs)
			}
			if sha := storedSHA(t); sha != tt.sha {
				t.Error("expected stored sha", tt.sha, "got", sha)
			}
			want := map[string]bool{}
			for _, commitID := range tt.logged {
				want[commitID] = true
			}
			if got := logged(t); !maps.Equal(got, want) {
				t.Error("expected logged commits", tt.logged, "got", slices.Sorted(maps.Keys(got)))
			}
			var messages []string
			if !tt.fails && tt.result == Committed {
				messages = append(messages, REPOS_UPDATE_PREFIX+":"+key)
				if tt.reloads {
					messages = append(messages, COMMITS_UPDATE_PREFIX)
				}
			}
			if got := published(); !slices.Equal(got, messages) {
				t.Errorf("expected published %q got %q", messages, got)
			}
		})
	}

	t.Run("corrupt stored repo fails without writing", func(t *testing.T) {
		if err := rdb.FlushDB(t.Context()).Err(); err != nil {
			t.Fatal(err)
		}
		rdb.HSet(t.Context(), REPOS_KEY, key, "not json")
		r := &Repos{client: FromRedisClient(rdb), logger: discard}
		if _, err := r.commitDocs(t.Context(), next, "a", map[string]string{"docs/install/linux.md": "x"}, []ConventionalCommit{c2}); err == nil {
			t.Error("expected an error for a stored repo that is not JSON")
		}
		if n := rdb.Exists(t.Context(), docsKey, COMMITS_UPDATE_PREFIX).Val(); n != 0 {
			t.Error("expected no docs or commits written")
		}
	})
}

// scriptedGithub serves fixed files and answers each Compare with the next status in statuses, repeating the last.
type scriptedGithub struct {
	files    map[string]File
	statuses []string
	compares int
}

func (g *scriptedGithub) FetchFiles(ctx context.Context, owner, repo, sha string, paths ...string) (map[string]File, error) {
	return g.files, nil
}

func (g *scriptedGithub) Compare(ctx context.Context, owner, repo, base, head string) (*github.CommitsComparison, error) {
	status := g.statuses[min(g.compares, len(g.statuses)-1)]
	g.compares++
	return &github.CommitsComparison{Status: github.Ptr(status)}, nil
}

// [Repos.Set] restarts a push that deletes docs.toml when another push committed first, so the deep clean is decided
// by the comparison from the new LastIngestedSHA: still ahead deep cleans the repo, behind leaves it ingested.
func Test_set_restarts_a_deep_clean_another_push_overtook(t *testing.T) {
	rdb := startRedis(t)
	const id = 7
	key, docsKey := strconv.Itoa(id), strconv.Itoa(id)+DOCS_KEY_SUFFIX
	pushed := time.Unix(1_000_000, 0).UTC()

	tests := []struct {
		name     string
		statuses []string // the comparison from the cached SHA, then from the SHA that committed first
		removed  bool     // the repo is deep cleaned
	}{
		{"the push is still newer: deep cleaned from the new SHA", []string{"ahead", "ahead"}, true},
		{"the push that committed first is newer: stale, kept", []string{"ahead", "behind"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := rdb.FlushDB(t.Context()).Err(); err != nil {
				t.Fatal(err)
			}
			store := func(sha string) {
				t.Helper()
				data, err := json.Marshal(Repo{ID: id, Name: "my-lib", LastIngestedSHA: sha, LastPushedAt: pushed})
				if err != nil {
					t.Fatal(err)
				}
				if err := rdb.HSet(t.Context(), REPOS_KEY, key, data).Err(); err != nil {
					t.Fatal(err)
				}
			}
			store("a")
			if err := rdb.HSet(t.Context(), docsKey, README_PATH, "<h1>my-lib</h1>").Err(); err != nil {
				t.Fatal(err)
			}

			gh := &scriptedGithub{
				files:    map[string]File{DOCS_CONFIG_PATH: {Status: FileMissing}, README_PATH: {Status: FileFound, Data: []byte("# my-lib")}},
				statuses: tt.statuses,
			}
			r, err := New(t.Context(), FromRedisClient(rdb), gh, slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatal(err)
			}
			// another push commits b after the local cache read a, without the cache seeing it yet
			store("b")

			event := &github.PushEvent{After: github.Ptr("c"), Repo: &github.PushEventRepository{
				ID: github.Ptr(int64(id)), Name: github.Ptr("my-lib"), Owner: &github.User{Login: github.Ptr("owner")},
			}}
			if err := r.Set(t.Context(), event); err != nil {
				t.Fatal("expected no error, got", err)
			}

			if gh.compares != 2 {
				t.Error("expected a second comparison after the restart, got", gh.compares)
			}
			exists := rdb.HExists(t.Context(), REPOS_KEY, key).Val()
			if exists == tt.removed {
				t.Error("expected the repo removed", tt.removed, "but it exists", exists)
			}
		})
	}
}
