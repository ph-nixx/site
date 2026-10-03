package repos

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// mockClient is an in-memory Client with a controllable hash and subscription.
type mockClient struct {
	mu   sync.Mutex
	hash map[string]string
	sub  *mockSub
}

func newMockClient(hash map[string]string) *mockClient {
	return &mockClient{hash: hash, sub: newmockSub()}
}

func (f *mockClient) HGetAll(ctx context.Context, key string) *redis.MapStringStringCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]string, len(f.hash))
	for k, v := range f.hash {
		out[k] = v
	}
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

func (f *mockClient) Subscribe(ctx context.Context, channels ...string) (Subscription, error) {
	return f.sub, nil
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

func newmockSub() *mockSub {
	return &mockSub{ch: make(chan *redis.Message, 1), closed: make(chan struct{})}
}

func (s *mockSub) Publish(payload string)                                    { s.ch <- &redis.Message{Payload: payload} }
func (s *mockSub) Channel(opts ...redis.ChannelOption) <-chan *redis.Message { return s.ch }
func (s *mockSub) Close() error {
	s.closeOne.Do(func() { close(s.closed) })
	return nil
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
	r := Repos{cache: c}
	result := r.Get()
	if 2 != len(result) {
		t.Error("expected length 2", "got", result)
	}
	result = r.Get(2)
	if len(result) != 1 {
		t.Error("expected length 1", "got", result)
	}
}

// Calling [New] subscribes to the client's repos channel and [Repos.cache]
// is synced when the message is published to the repos channel externally.
func Test_new_repos_subscribes_and_syncs_local_cache(t *testing.T) {
	client := newMockClient(map[string]string{})
	client.set(t, Repo{ID: 1, Name: "one"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r, err := New(ctx, client)
	if err != nil {
		t.Fatal(err)
	}

	// Initial load comes from HGetAll.
	if got := r.Get(1); len(got) != 1 || got[0] == nil || got[0].Name != "one" {
		t.Fatalf("expected repo 1 loaded at startup, got %v", got)
	}

	// An external writer adds repo 2 and publishes its ID.
	client.set(t, Repo{ID: 2, Name: "two"})
	client.sub.Publish("2")

	eventually(t, func() bool {
		got := r.Get(2)
		return len(got) == 1 && got[0] != nil && got[0].Name == "two"
	}, "repo 2 to appear in local cache")

	// A malformed payload is skipped and does not stop the loop.
	client.sub.Publish("not-a-number")
	client.set(t, Repo{ID: 3, Name: "three"})
	client.sub.Publish("3")

	eventually(t, func() bool {
		got := r.Get(3)
		return len(got) == 1 && got[0] != nil
	}, "repo 3 to appear after a malformed message")

	// Cancelling the context stops the loop and closes the subscription.
	cancel()
	select {
	case <-client.sub.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("subscription was not closed after context cancel")
	}
}
