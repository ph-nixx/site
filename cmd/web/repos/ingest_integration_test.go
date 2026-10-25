//go:build integration

package repos

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

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
// successful commit applies together, and the retries after a WATCH abort.
func Test_commit_docs_against_redis(t *testing.T) {
	rdb := startRedis(t)
	const id = 7
	key, docsKey := strconv.Itoa(id), strconv.Itoa(id)+DOCS_KEY_SUFFIX
	pushed := time.Unix(1_000_000, 0).UTC()

	// writes repo and its docs as a previous commit would have
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
	// subscribes to the updates channel; the returned func reports the next message, or "" after a short wait
	subscribe := func(t *testing.T) func() string {
		t.Helper()
		sub := rdb.Subscribe(t.Context(), REPOS_UPDATES_CHANNEL)
		t.Cleanup(func() { sub.Close() })
		if _, err := sub.Receive(t.Context()); err != nil { // the subscription confirmation
			t.Fatal(err)
		}
		return func() string {
			msg, err := sub.ReceiveTimeout(t.Context(), 200*time.Millisecond)
			if m, ok := msg.(*redis.Message); ok && err == nil {
				return m.Payload
			}
			return ""
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
		aborts   int // WATCH aborts injected before the transaction can EXEC
		result   CommitResult
		fails    bool              // commitDocs returns an error
		docs     map[string]string // the docs hash afterwards
		sha      string            // the stored LastIngestedSHA afterwards
	}{
		{
			name: "first ingest writes only the referenced html", repo: stored, expected: "",
			html:   map[string]string{README_PATH: "<h1>readme</h1>", "docs/install/linux.md": "<p>linux</p>", "docs/skipped.md": "<p>skipped</p>"},
			result: Committed, docs: map[string]string{README_PATH: "<h1>readme</h1>", "docs/install/linux.md": "<p>linux</p>"}, sha: "a",
		},
		{
			name: "commit prunes old paths and keeps frozen ones and README.md", seed: true, repo: next, expected: "a",
			html:   map[string]string{"docs/install/linux.md": "<p>linux v2</p>"},
			result: Committed, sha: "b",
			docs: map[string]string{README_PATH: "<h1>readme</h1>", "docs/install/linux.md": "<p>linux v2</p>", "docs/install/macos.md": "<p>macos</p>"},
		},
		{
			name: "metadata only change rewrites just the repo", seed: true, repo: Repo{ID: id, LastIngestedSHA: "b", LastPushedAt: pushed.Add(time.Minute), Title: "New", DocSections: stored.DocSections},
			expected: "a", result: Committed, docs: storedDocs, sha: "b",
		},
		{
			name: "a later push already committed: superseded", seed: true, repo: Repo{ID: id, LastIngestedSHA: "c", LastPushedAt: pushed.Add(-time.Minute)},
			expected: "z", result: Superseded, docs: storedDocs, sha: "a",
		},
		{
			name: "pushed at the same second: overtaken", seed: true, repo: Repo{ID: id, LastIngestedSHA: "c", LastPushedAt: pushed},
			expected: "z", result: Overtaken, docs: storedDocs, sha: "a",
		},
		{
			name: "an earlier push committed first: overtaken", seed: true, repo: Repo{ID: id, LastIngestedSHA: "c", LastPushedAt: pushed.Add(time.Minute)},
			expected: "z", result: Overtaken, docs: storedDocs, sha: "a",
		},
		{
			name: "a deep clean committed first: overtaken", repo: next, expected: "a",
			html: map[string]string{"docs/install/linux.md": "<p>linux v2</p>"}, result: Overtaken, docs: map[string]string{}, sha: "",
		},
		{
			name: "aborted EXEC is retried until it commits", seed: true, repo: next, expected: "a", aborts: TX_RETRIES,
			result: Committed, sha: "b",
			docs: map[string]string{README_PATH: "<h1>readme</h1>", "docs/install/linux.md": "<p>linux</p>", "docs/install/macos.md": "<p>macos</p>"},
		},
		{
			name: "aborted EXEC past the retry cap fails", seed: true, repo: next, expected: "a", aborts: TX_RETRIES + 1,
			fails: true, docs: storedDocs, sha: "a",
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
			r := &Repos{client: &interferingClient{Client: FromRedisClient(rdb), rdb: rdb, aborts: tt.aborts}}

			result, err := r.commitDocs(t.Context(), tt.repo, tt.expected, tt.html)
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
			want := ""
			if !tt.fails && tt.result == Committed {
				want = REPOS_UPDATE_PREFIX + ":" + key
			}
			if got := published(); got != want {
				t.Errorf("expected published %q got %q", want, got)
			}
		})
	}

	t.Run("corrupt stored repo fails without writing", func(t *testing.T) {
		if err := rdb.FlushDB(t.Context()).Err(); err != nil {
			t.Fatal(err)
		}
		rdb.HSet(t.Context(), REPOS_KEY, key, "not json")
		r := &Repos{client: FromRedisClient(rdb)}
		if _, err := r.commitDocs(t.Context(), next, "a", map[string]string{"docs/install/linux.md": "x"}); err == nil {
			t.Error("expected an error for a stored repo that is not JSON")
		}
		if n := rdb.Exists(t.Context(), docsKey).Val(); n != 0 {
			t.Error("expected no docs written")
		}
	})
}
