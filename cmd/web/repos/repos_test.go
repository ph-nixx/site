package repos

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// mockClient is an in-memory Client with a controllable hash, commit set and subscription.
type mockClient struct {
	mu        sync.Mutex
	hash      map[string]string
	commits   []string // commit JSON strings in the order ZRANGE ... REV would return them
	zrangeErr error
	lastZ     redis.ZRangeArgs
	sub       *mockSub
}

func newMockClient(hash map[string]string) *mockClient {
	return &mockClient{hash: hash, sub: newMockSub()}
}

func (f *mockClient) HGetAll(ctx context.Context, key string) *redis.MapStringStringCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	maps.Copy(out, f.hash)
	cmd := redis.NewMapStringStringCmd(ctx, "hgetall", key)
	cmd.SetVal(out)
	return cmd
}

func (f *mockClient) HGet(ctx context.Context, key, field string) *redis.StringCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := redis.NewStringCmd(ctx, "hget", key, field)
	v, ok := f.hash[field]
	if !ok {
		cmd.SetErr(redis.Nil)
		return cmd
	}
	cmd.SetVal(v)
	return cmd
}

func (f *mockClient) Subscribe(ctx context.Context, channels ...string) Subscription {
	return f.sub
}

func (f *mockClient) ZRangeArgs(ctx context.Context, z redis.ZRangeArgs) *redis.StringSliceCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastZ = z
	cmd := redis.NewStringSliceCmd(ctx, "zrange", z.Key)
	if f.zrangeErr != nil {
		cmd.SetErr(f.zrangeErr)
		return cmd
	}
	cmd.SetVal(append([]string(nil), f.commits...))
	return cmd
}

// setCommits replaces the commit set; pass raw JSON strings, newest first.
func (f *mockClient) setCommits(vals ...string) {
	f.mu.Lock()
	f.commits = vals
	f.mu.Unlock()
}

// commitJSON marshals a commit the way the external writer would store it.
func commitJSON(t *testing.T, c ConventionalCommit) string {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// set writes a repo into the mock hash, as an external writer would before publishing.
func (f *mockClient) set(t *testing.T, repo Repo) {
	t.Helper()
	b, err := json.Marshal(repo)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.hash[strconv.FormatInt(repo.ID, 10)] = string(b)
	f.mu.Unlock()
}

// mockSub is a Subscription the test drives by hand.
type mockSub struct {
	ch       chan *redis.Message
	closed   chan struct{}
	closeOne sync.Once
}

func newMockSub() *mockSub {
	return &mockSub{ch: make(chan *redis.Message, 1), closed: make(chan struct{})}
}

func (s *mockSub) Publish(payload string) {
	s.ch <- &redis.Message{Payload: payload}
}
func (s *mockSub) Channel(opts ...redis.ChannelOption) <-chan *redis.Message {
	return s.ch
}
func (s *mockSub) Close() error {
	s.closeOne.Do(func() { close(s.closed) })
	return nil
}
func (s *mockSub) Receive(ctx context.Context) (any, error) {
	return nil, nil
}

// eventually polls cond until it is true or the deadline passes.
func eventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for:", msg)
}

func Test_get_returns_repo_subject_to_variadic_arg_convention(t *testing.T) {
	c := map[int64]*Repo{1: nil, 2: nil}
	r := Repos{repoCache: c}
	result := r.Get()
	if 2 != len(result) {
		t.Error("expected length 2", "got", result)
	}
	result = r.Get(2)
	if len(result) != 1 {
		t.Error("expected length 1", "got", result)
	}
}

// Calling [New] subscribes to the client's repos channel and [Repos.repoCache]
// is synced when messages are published to the repos updates channel.
func Test_new_repos_subscribes_and_syncs_local_cache(t *testing.T) {
	client := newMockClient(map[string]string{})
	client.set(t, Repo{ID: 1, Name: "one"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r, err := New(ctx, client, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	// Initial load comes from HGetAll.
	if got := r.Get(1); len(got) != 1 || got[0] == nil || got[0].Name != "one" {
		t.Fatalf("expected repo 1 loaded at startup, got %v", got)
	}

	// An external writer adds repo 2 and publishes its update message.
	client.set(t, Repo{ID: 2, Name: "two"})
	client.sub.Publish("repos:2")

	eventually(t, func() bool {
		got := r.Get(2)
		return len(got) == 1 && got[0] != nil && got[0].Name == "two"
	}, "repo 2 to appear in local cache")

	// Malformed payloads (no ID separator, non-numeric ID) are skipped and do not stop the loop.
	client.sub.Publish("repos")
	client.sub.Publish("repos:not-a-number")
	client.set(t, Repo{ID: 3, Name: "three"})
	client.sub.Publish("repos:3")

	eventually(t, func() bool {
		got := r.Get(3)
		return len(got) == 1 && got[0] != nil
	}, "repo 3 to appear after a malformed message")

	// Cancelling the context stops the loop and closes the subscription.
	cancel()
	select {
	case <-client.sub.closed:
	case <-time.After(1 * time.Second):
		t.Fatal("subscription was not closed after context cancel")
	}
}

// Calling [New] loads the aggregated commits (skipping undecodable entries) and
// [Repos.commitCache] is refetched when the commits update message is published.
func Test_new_repos_loads_commits_and_resyncs_on_update(t *testing.T) {
	client := newMockClient(map[string]string{})
	client.setCommits(
		commitJSON(t, ConventionalCommit{ID: "b", Body: "newer"}),
		"not json",
		commitJSON(t, ConventionalCommit{ID: "a", Body: "older"}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r, err := New(ctx, client, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	got := r.Commits()
	if len(got) != 2 || got[0].ID != "b" || got[1].ID != "a" {
		t.Fatalf("expected commits [b a] loaded at startup, got %v", got)
	}

	client.mu.Lock()
	z := client.lastZ
	client.mu.Unlock()
	if z.Key != COMMITS_UPDATE_PREFIX || !z.Rev || z.Start != 0 || z.Stop != -1 {
		t.Errorf("unexpected ZRangeArgs %+v, want key %q, rev, 0..-1", z, COMMITS_UPDATE_PREFIX)
	}

	// An external writer adds a newer commit and announces it.
	client.setCommits(
		commitJSON(t, ConventionalCommit{ID: "c", Body: "newest"}),
		commitJSON(t, ConventionalCommit{ID: "b", Body: "newer"}),
		commitJSON(t, ConventionalCommit{ID: "a", Body: "older"}),
	)
	client.sub.Publish(COMMITS_UPDATE_PREFIX)

	eventually(t, func() bool {
		got := r.Commits()
		return len(got) == 3 && got[0].ID == "c"
	}, "commit cache to refresh after the commits update message")
}

func Test_new_repos_returns_error_when_commits_fetch_fails(t *testing.T) {
	client := newMockClient(map[string]string{})
	client.zrangeErr = errors.New("boom")

	if _, err := New(context.Background(), client, slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("expected New to fail when the commits fetch fails")
	}
}
