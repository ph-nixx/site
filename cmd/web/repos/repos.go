package repos

import (
	"context"
	"encoding/json"
	"log/slog"
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

type Repos struct {
	client      Client
	repoCache   RepoCache
	commitCache CommitCache
	mu          sync.RWMutex
	logger      *slog.Logger
}

// A mapping from the numeric ID of a Repo to its fields synced with Redis by [Repos.sync_local_cache].
type RepoCache = map[int64]*Repo

// The n most chronologically recent [Commit] in Redis.
type CommitCache = []Commit

// The subset of [redis.Client] functionality that Repos needs.
type Client interface {
	HGetAll(ctx context.Context, key string) *redis.MapStringStringCmd
	HGet(ctx context.Context, key, field string) *redis.StringCmd
	Subscribe(ctx context.Context, channels ...string) Subscription
}

type Subscription interface {
	Channel(opts ...redis.ChannelOption) <-chan *redis.Message
	Close() error
	Receive(ctx context.Context) (any, error)
}

// redisClient adapts *redis.Client to Client. The embedded client supplies
// HGetAll and HGet directly; only Subscribe is overridden.
type redisClient struct {
	*redis.Client
}

// Coerces a go-redis client as a Client.
func FromRedisClient(rdb *redis.Client) Client {
	return &redisClient{Client: rdb}
}

// Subscribe shadows the embedded client's Subscribe, which returns *redis.PubSub,
// so the result can be returned as a Subscription.
func (c *redisClient) Subscribe(ctx context.Context, channels ...string) Subscription {
	return c.Client.Subscribe(ctx, channels...)
}

// Repo is a repository as returned by the GitHub REST API.
type Repo struct {
	ID            int64  `json:"id"`
	SVG           string `json:"svg"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Description   string `json:"description"`
	Language      string `json:"language"`
	DefaultBranch string `json:"default_branch"`
	HeadCommit    Commit `json:"head_commit"`
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

func New(ctx context.Context, client Client, logger *slog.Logger) (*Repos, error) {
	r := Repos{
		client:      client,
		repoCache:   RepoCache{},
		commitCache: CommitCache{},
		logger:      logger.With("package", "repos"),
	}
	reposHash, err := r.client.HGetAll(ctx, REPOS_KEY).Result()
	if err != nil {
		return nil, err
	}

	for _, v := range reposHash {
		var repo Repo
		if err := json.Unmarshal([]byte(v), &repo); err != nil {
			return nil, err
		}
		r.repoCache[repo.ID] = &repo
	}
	sub := client.Subscribe(ctx, REPOS_UPDATES_CHANNEL)
	if _, err := sub.Receive(ctx); err != nil {
		sub.Close()
		return nil, err
	}

	go r.sync_local_cache(ctx, sub)
	return &r, nil
}

// Blocks and listens for updates pushed on [REPOS_UPDATES_CHANNEL], and mutates [Repos.repoCache]
// subject to the message published.
// During local cache reconciliation [sync.RWMutex.Lock] is called and blocks all other goroutines from
// reading or writing to [Repos.repoCache].
// NOTE: All [redis.Message] payloads published to [REPOS_UPDATES_CHANNEL] will only be instances of [Repo.ID]
func (r *Repos) sync_local_cache(ctx context.Context, sub Subscription) {
	defer sub.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-sub.Channel():
			if !ok {
				return
			}

			_, err := strconv.ParseInt(msg.Payload, 10, 64)
			if err != nil {
				r.logger.Warn("malformed repo ID published to updates channel", "payload", msg.Payload, "err", err)
				continue
			}

			repo_bytes, err := r.client.HGet(ctx, REPOS_KEY, msg.Payload).Bytes()
			if err != nil {
				r.logger.Warn("failed to fetch repo from Redis", "id", msg.Payload, "err", err)
				continue
			}

			var repo Repo
			if err := json.Unmarshal(repo_bytes, &repo); err != nil {
				r.logger.Warn("invalid repo JSON in Redis", "id", msg.Payload, "err", err)
				continue
			}
			r.mu.Lock()
			r.repoCache[repo.ID] = &repo
			r.mu.Unlock()
		}
	}
}

// Get a subset of repos from [Repos.repoCache] or the entire set if no IDs are provided.
// This function does block if [Repos.repoCache] is being synced with the Redis cache.
func (r *Repos) Get(IDs ...int64) []*Repo {
	if IDs == nil {
		r.mu.RLock() // NOTE: need to lock before alloc because mutation may change the number of pairs
		repos := make([]*Repo, 0, len(r.repoCache))

		// NOTE: the order of elements returned by range map[A]B is not guaranteed
		// I could fix this by sorting repos before returning it,
		// but the random order is a cool styling feature for the project catalog down stream
		for _, v := range r.repoCache {
			repos = append(repos, v)
		}
		r.mu.RUnlock()
		return repos
	}

	r.mu.RLock()
	repos := make([]*Repo, 0, len(IDs))
	for _, ID := range IDs {
		if val, ok := r.repoCache[ID]; ok {
			repos = append(repos, val)
		}
	}
	r.mu.RUnlock()
	return repos
}

// Get the aggregated commit log from the set of repos in chronological order.
func (r *Repos) Commits() []Commit {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.commitCache
}
