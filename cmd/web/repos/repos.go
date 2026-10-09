package repos

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
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

	REPOS_UPDATE_PREFIX   = "repos"
	COMMITS_UPDATE_PREFIX = "commits"
)

type Repos struct {
	client      Client
	repoCache   RepoCache
	commitCache []ConventionalCommit
	mu          sync.RWMutex
	logger      *slog.Logger
}

// A mapping from the numeric ID of a Repo to its fields synced with Redis by [Repos.sync_local_cache].
type RepoCache = map[int64]*Repo

// The subset of [redis.Client] functionality that Repos needs.
type Client interface {
	HGetAll(ctx context.Context, key string) *redis.MapStringStringCmd
	HGet(ctx context.Context, key, field string) *redis.StringCmd
	Subscribe(ctx context.Context, channels ...string) Subscription
	ZRangeArgs(ctx context.Context, z redis.ZRangeArgs) *redis.StringSliceCmd
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

func (r *redisClient) Subscribe(ctx context.Context, channels ...string) Subscription {
	return r.Client.Subscribe(ctx, channels...)
}

func (r *redisClient) ZRangeArgs(ctx context.Context, z redis.ZRangeArgs) *redis.StringSliceCmd {
	return r.Client.ZRangeArgs(ctx, z)
}

// A representation of a Git repo that is intended to be displayed as a project.
// NOTE: data modeling is not done
type Repo struct {
	ID          int64     `json:"id"`
	SVG         string    `json:"svg"`
	Name        string    `json:"name"`
	FullName    string    `json:"full_name"`
	Description string    `json:"description"`
	LastCommit  time.Time `json:"last_commit"`
}

// A representation of a Git commit that conforms to the
// [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/O) standard.
// NOTE: data modeling is not done
type ConventionalCommit struct {
	ID        string    `json:"id"`
	RepoName  string    `json:"repo_name"`
	Timestamp time.Time `json:"timestamp"`

	// <[ConventionalCommit.Type]>(optional [ConventionalCommit.Scope]): <[ConventionalCommit.Description]>
	//
	// [ConventionalCommit.Body]

	Type        string `json:"type"`
	Scope       string `json:"scope"`
	Description string `json:"description"`
	Body        string `json:"body"`
}

type GithubPushEvent struct {
	Repo       GithubRepo     `json:"repository"`
	HeadCommit GithubCommit   `json:"head_commit"`
	Commits    []GithubCommit `json:"commits"`
}

// Repository as returned by the GitHub REST API.
type GithubRepo struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	FullName    string `json:"full_name"`
	Description string `json:"description"`
	Language    string `json:"language"`
}

// Commit is a single pushed commit as returned by a GitHub push event.
type GithubCommit struct {
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
		commitCache: []ConventionalCommit{},
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
	r.commitCache, err = r.fetchCommits(ctx)
	if err != nil {
		return nil, err
	}

	sub := client.Subscribe(ctx, REPOS_UPDATES_CHANNEL)
	if _, err := sub.Receive(ctx); err != nil {
		sub.Close()
		return nil, err
	}

	go r.syncLocalCache(ctx, sub)
	return &r, nil
}

// fetchCommits loads the aggregated commit history from Redis in newest to oldest order,
// skipping entries that fail to decode.
func (r *Repos) fetchCommits(ctx context.Context) ([]ConventionalCommit, error) {
	args := redis.ZRangeArgs{Key: COMMITS_UPDATE_PREFIX, Start: 0, Stop: -1, Rev: true}
	jsonCommits, err := r.client.ZRangeArgs(ctx, args).Result()
	if err != nil {
		return nil, err
	}

	commits := make([]ConventionalCommit, 0, len(jsonCommits))
	for _, val := range jsonCommits {
		var c ConventionalCommit
		if err := json.Unmarshal([]byte(val), &c); err != nil {
			r.logger.Warn("failed to Unmarshal fetched commit JSON string", "content", val, "err", err)
			continue
		}
		commits = append(commits, c)
	}
	return commits, nil
}

// Blocks and listens for updates pushed on [REPOS_UPDATES_CHANNEL], and mutates [Repos.repoCache]
// subject to the message published.
// During local cache reconciliation [sync.RWMutex.Lock] is called and blocks all other goroutines from
// reading or writing to [Repos.repoCache].
// NOTE: [redis.Message.Payload] format is assumed to be UPDATE_TYPE | UPDATE_TYPE:[Repo.ID]
func (r *Repos) syncLocalCache(ctx context.Context, sub Subscription) {
	defer sub.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-sub.Channel():
			if !ok {
				return
			}

			if strings.HasPrefix(msg.Payload, REPOS_UPDATE_PREFIX) {
				_, repoID, ok := strings.Cut(msg.Payload, ":")
				if !ok {
					r.logger.Warn("malformed repo update message published to updates channel", "payload", msg.Payload)
					continue
				}

				_, err := strconv.ParseInt(repoID, 10, 64)
				if err != nil {
					r.logger.Warn("malformed repo ID published to updates channel", "payload", msg.Payload, "err", err)
					continue
				}

				repo_bytes, err := r.client.HGet(ctx, REPOS_KEY, repoID).Bytes()
				if err != nil {
					r.logger.Warn("failed to fetch repo from Redis", "id", repoID, "err", err)
					continue
				}

				var repo Repo
				if err := json.Unmarshal(repo_bytes, &repo); err != nil {
					r.logger.Warn("invalid repo JSON in Redis", "id", repoID, "err", err)
					continue
				}

				r.mu.Lock()
				r.repoCache[repo.ID] = &repo
				r.mu.Unlock()
			} else if msg.Payload == COMMITS_UPDATE_PREFIX {
				commits, err := r.fetchCommits(ctx)
				if err != nil {
					r.logger.Warn("failed to fetch commits from Redis", "payload", msg.Payload, "err", err)
					continue
				}

				r.mu.Lock()
				r.commitCache = commits
				r.mu.Unlock()
			}
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

// Yields the aggregated commits from the set of repos in newest to oldest order.
func (r *Repos) Commits() []ConventionalCommit {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.commitCache
}

// Reconciles the Redis cache via updating the Repo JSON string or fetching and parsing new or changed
// Markdown files to HTML strings, minimal work is done when nothing is changed.
func (r *Repos) Set(ctx context.Context, event GithubPushEvent) error {
	return nil
}
