package repos

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// The key for the redis Hash that contains all the Repo JSON strings.
	// HGET {REPOS_KEY} {Repo.ID} -> JSON string
	// HGET {Repo.ID} path:to:file.md -> JSON string | HTML string
	REPOS_KEY = "repos"

	// When the state of any repo is changed in Redis, a message describing the change
	// is published to this channel.
	REPOS_UPDATES_CHANNEL = "repos:updates"
)

// Subscription is a live feed of messages from one channel.
type Subscription interface {
	Channel(opts ...redis.ChannelOption) <-chan *redis.Message
	Close() error
}

// The subset of [redis.Client] functionality that Repos needs.
type Client interface {
	HGetAll(ctx context.Context, key string) *redis.MapStringStringCmd
	HGet(ctx context.Context, key, field string) *redis.StringCmd
	// Subscribe returns only once the subscription is confirmed.
	Subscribe(ctx context.Context, channels ...string) (Subscription, error)
}

// redisClient adapts *redis.Client to Client. The embedded client supplies
// HGetAll and HGet directly; only Subscribe is overridden.
type redisClient struct {
	*redis.Client
}

// Wraps a [*redis.Client] client as a Client.
func FromRedisClient(rdb *redis.Client) Client {
	return &redisClient{Client: rdb}
}

// Subscribe opens a PubSub, waits for confirmation, and adapts it to Subscription.
// It shadows the embedded client's Subscribe, which returns *redis.PubSub.
func (s *redisClient) Subscribe(ctx context.Context, channels ...string) (Subscription, error) {
	ps := s.Client.Subscribe(ctx, channels...)
	if _, err := ps.Receive(ctx); err != nil {
		ps.Close()
		return nil, err
	}
	return ps, nil
}

type Repos struct {
	client Client
	cache  LocalCache
	mu     sync.RWMutex
}

// A mapping from the numeric ID of a Repo to its fields synced with Redis by [Repos.sync_local_cache].
type LocalCache = map[int64]*Repo

// Repo is a repository as returned by the GitHub REST API.
type Repo struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Description   string `json:"description"`
	Language      string `json:"language"`
	DefaultBranch string `json:"default_branch"`
}

// Commit is a single pushed commit as reported by a GitHub push event.
type Commit struct {
	ID        string    `json:"id"`
	RepoName  string    `json:"repo_name"`
	Timestamp time.Time `json:"timestamp"`
	Author    Author    `json:"author"`
	Distinct  bool      `json:"distinct"`
	Message   string    `json:"message"`
	Added     []string  `json:"added"`
	Modified  []string  `json:"modified"`
	Removed   []string  `json:"removed"`
}

type Author struct {
	Username string `json:"username"`
	Email    string `json:"email"`
}

func New(ctx context.Context, client Client) (*Repos, error) {
	r := Repos{client: client, cache: LocalCache{}}
	reposHash, err := r.client.HGetAll(ctx, REPOS_KEY).Result()
	if err != nil {
		return nil, err
	}

	for _, v := range reposHash {
		var repo Repo
		if err := json.Unmarshal([]byte(v), &repo); err != nil {
			panic(err)
		}
		r.cache[repo.ID] = &repo
	}

	sub, err := r.client.Subscribe(ctx, REPOS_UPDATES_CHANNEL)
	if err != nil {
		return nil, err
	}

	go r.sync_local_cache(ctx, sub)
	return &r, nil
}

// Blocks and listens for updates pushed on [REPOS_UPDATES_CHANNEL], and mutates [Repos.cache]
// subject to the message published.
// During local cache reconciliation [sync.RWMutex.Lock] is called and blocks all other goroutines from
// reading or writing to [Repos.cache].
// NOTE: All [redis.Message] payloads published to [REPOS_UPDATES_CHANNEL] will only be instances of [Repo.ID]
func (r *Repos) sync_local_cache(ctx context.Context, sub Subscription) {
	defer sub.Close()
	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			_, err := strconv.ParseInt(msg.Payload, 10, 64)
			if err != nil {
				continue // NOTE: this means a message published to the channel was malformed, idk how to handle this rn
			}

			repo_bytes, err := r.client.HGet(ctx, REPOS_KEY, msg.Payload).Bytes()
			if err != nil {
				continue
			}

			var repo Repo
			json.Unmarshal(repo_bytes, &repo)
			r.mu.Lock()
			r.cache[repo.ID] = &repo
			r.mu.Unlock()
		}
	}
}

// Get a subset of repos from [Repos.cache] or the entire set if no IDs are provided.
// This function does block if [Repos.cache] is being synced with the Redis cache.
func (r *Repos) Get(IDs ...int64) []*Repo {
	if IDs == nil {
		r.mu.RLock() // NOTE: need to lock before alloc because mutation may change the number of pairs
		repos := make([]*Repo, 0, len(r.cache))
		for _, v := range r.cache {
			repos = append(repos, v)
		}
		r.mu.RUnlock()
		return repos
	}
	r.mu.RLock()
	repos := make([]*Repo, 0, len(IDs))
	for _, ID := range IDs {
		repos = append(repos, r.cache[ID])
	}
	r.mu.RUnlock()
	return repos
}
