package repos

import (
	"context"
	"encoding/json"
	"errors"
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

	// Suffix of the per repo hash of rendered docs.
	// HGET {Repo.ID}{DOCS_KEY_SUFFIX} path/to/file.md -> HTML string
	DOCS_KEY_SUFFIX = ":docs"

	// Repo root relative paths fetched on every push.
	DOCS_CONFIG_PATH = "docs.toml"
	README_PATH      = "README.md"

	// Starting values from the RFC; adjust once real ingestion times are known.
	MAX_RESTARTS       = 3                   // Overtaken restarts before Set fails
	TX_RETRIES         = 3                   // WATCH aborts retried inside one transaction
	COMPARE_FILE_LIMIT = 300                 // a comparison with this many files may be truncated
	MAX_DOC_DEPTH      = 3                   // chapter, section, subsection
	COMMIT_RETENTION   = 90 * 24 * time.Hour // commits older than this are pruned from the log
)

var (
	ErrUnknownRepo = errors.New("unknown repo") // respond 404
	ErrUnknownPath = errors.New("unknown path") // redirect to /{repo name}
)

// The local cache of every repo and the commit log, synced with Redis by syncLocalCache.
type Repos struct {
	client      Client               // Redis
	github      Github               // GitHub REST API calls used by ingestion
	repoCache   RepoCache            // Repo.ID -> repo
	names       map[string]int64     // Repo.Name -> Repo.ID, used only to resolve the first URL segment
	commitCache []ConventionalCommit // newest to oldest
	mu          sync.RWMutex         // guards repoCache, names, and commitCache, which are updated together
}

// A mapping from the numeric ID of a Repo to its fields synced with Redis by [Repos.sync_local_cache].
type RepoCache = map[int64]*Repo

// The subset of [redis.Client] functionality that Repos needs.
type Client interface {
	HGetAll(ctx context.Context, key string) *redis.MapStringStringCmd
	HGet(ctx context.Context, key, field string) *redis.StringCmd
	Subscribe(ctx context.Context, channels ...string) Subscription
	ZRangeArgs(ctx context.Context, z redis.ZRangeArgs) *redis.StringSliceCmd
	Watch(ctx context.Context, fn func(*redis.Tx) error, keys ...string) error
	ZRange(ctx context.Context, key string, start, stop int64) *redis.StringSliceCmd
	ZAdd(ctx context.Context, key string, members ...redis.Z) *redis.IntCmd
	ZRemRangeByScore(ctx context.Context, key, min, max string) *redis.IntCmd
	Publish(ctx context.Context, channel string, message any) *redis.IntCmd
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

// A Git repo displayed as a project, stored as a JSON string in the `repos` hash.
type Repo struct {
	ID              int64        `json:"id"`                // immutable GitHub repo ID
	Name            string       `json:"name"`              // GitHub repo name, the first URL segment
	FullName        string       `json:"full_name"`         // owner/name, used for GitHub API requests
	Title           string       `json:"title"`             // [project].title, or Name with the first character capitalized
	Description     string       `json:"description"`       // GitHub repo description
	SVG             string       `json:"svg"`               // [project].svg
	DocSections     []DocSection `json:"doc_sections"`      // chapters as served, in docs.toml order: invalid nodes frozen or skipped
	LastCommit      time.Time    `json:"last_commit"`       // timestamp of the push's head commit
	LastIngestedSHA string       `json:"last_ingested_sha"` // last commit whose docs were fully ingested
	LastPushedAt    time.Time    `json:"last_pushed_at"`    // when GitHub received the push at LastIngestedSHA; orders pushes the comparison cannot

	// Derived by the local cache on load, never written to Redis.
	DocRoutes map[string]string `json:"-"` // stacked segments to file path, e.g. "install/linux" -> "docs/install/linux.md"; "" -> README.md
}

// A chapter, section, or subsection parsed from docs.toml.
type DocSection struct {
	Frozen      bool         `json:"frozen,omitempty"` // kept from the last ingest because the node is currently invalid
	Segment     string       `json:"segment"`          // this node's URL segment, without its ancestors
	Title       string       `json:"title"`            // sidebar text
	Path        string       `json:"path"`             // repo root relative file path; empty for a heading only chapter
	SubSections []DocSection `json:"sub_sections"`     // children in docs.toml order, at most depth 3 in total
}

// A representation of a Git commit that conforms to the
// [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/O) standard.
type ConventionalCommit struct {
	ID        string    `json:"id"`
	RepoID    int64     `json:"repo_id"`   // matches Repo.ID, used by the deep clean and to look up the repo's current name
	Timestamp time.Time `json:"timestamp"` // committer date, never a time derived from the push

	// <[ConventionalCommit.Type]>(optional [ConventionalCommit.Scope]): <[ConventionalCommit.Description]>
	//
	// [ConventionalCommit.Body]

	Type        string `json:"type"`
	Scope       string `json:"scope"`
	Description string `json:"description"`
	Body        string `json:"body"`
}

func New(ctx context.Context, client Client, gh Github, logger *slog.Logger) (*Repos, error) {
	r := Repos{
		client:      client,
		github:      gh,
		repoCache:   RepoCache{},
		names:       map[string]int64{},
		commitCache: []ConventionalCommit{},
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
	r.commitCache, err = r.fetchCommits(ctx, logger)
	if err != nil {
		return nil, err
	}

	sub := client.Subscribe(ctx, REPOS_UPDATES_CHANNEL)
	if _, err := sub.Receive(ctx); err != nil {
		sub.Close()
		return nil, err
	}

	go r.syncLocalCache(ctx, sub, logger)
	return &r, nil
}

// fetchCommits loads the aggregated commit history from Redis in newest to oldest order,
// skipping entries that fail to decode.
func (r *Repos) fetchCommits(ctx context.Context, logger *slog.Logger) ([]ConventionalCommit, error) {
	args := redis.ZRangeArgs{Key: COMMITS_UPDATE_PREFIX, Start: 0, Stop: -1, Rev: true}
	jsonCommits, err := r.client.ZRangeArgs(ctx, args).Result()
	if err != nil {
		return nil, err
	}

	commits := make([]ConventionalCommit, 0, len(jsonCommits))
	for _, val := range jsonCommits {
		var c ConventionalCommit
		if err := json.Unmarshal([]byte(val), &c); err != nil {
			logger.Warn("failed to Unmarshal fetched commit JSON string", "content", val, "err", err)
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
func (r *Repos) syncLocalCache(ctx context.Context, sub Subscription, logger *slog.Logger) {
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
					logger.Warn("malformed repo update message published to updates channel", "payload", msg.Payload)
					continue
				}

				_, err := strconv.ParseInt(repoID, 10, 64)
				if err != nil {
					logger.Warn("malformed repo ID published to updates channel", "payload", msg.Payload, "err", err)
					continue
				}

				repo_bytes, err := r.client.HGet(ctx, REPOS_KEY, repoID).Bytes()
				if err != nil {
					logger.Warn("failed to fetch repo from Redis", "id", repoID, "err", err)
					continue
				}

				var repo Repo
				if err := json.Unmarshal(repo_bytes, &repo); err != nil {
					logger.Warn("invalid repo JSON in Redis", "id", repoID, "err", err)
					continue
				}

				r.mu.Lock()
				r.repoCache[repo.ID] = &repo
				r.mu.Unlock()
			} else if msg.Payload == COMMITS_UPDATE_PREFIX {
				commits, err := r.fetchCommits(ctx, logger)
				if err != nil {
					logger.Warn("failed to fetch commits from Redis", "payload", msg.Payload, "err", err)
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
func (r *Repos) Get(IDs ...int64) ([]*Repo, bool) {
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
		return repos, len(repos) > 0
	}

	r.mu.RLock()
	repos := make([]*Repo, 0, len(IDs))
	for _, ID := range IDs {
		if val, ok := r.repoCache[ID]; ok {
			repos = append(repos, val)
		}
	}
	r.mu.RUnlock()
	return repos, len(repos) > 0
}

// A [Repos.Get] wrapper that uses [Repos.names] to do the [Repo.Name] -> [Repo.ID] mapping.
func (r *Repos) GetByName(name string) (*Repo, bool) {
	ID, ok := r.names[name]
	if !ok {
		return nil, false
	}

	repo, ok := r.Get(ID)
	if ok {
		return repo[0], ok
	}

	return nil, false
}

// Yields the aggregated commits from the set of repos in newest to oldest order.
func (r *Repos) Commits() []ConventionalCommit {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.commitCache
}

// Builds a cache ready repo from its stored JSON: decodes it and maps every node with a non empty Path (plus the project
// page under "") to its file path keyed by stacked segments in DocRoutes.
func newRepo(data []byte) (*Repo, error) {
	panic("unimplemented")
}

// Stores a derived repo in the local cache by ID and maps its name to that ID, removing its old name if it was renamed.
func (r *Repos) replace(repo *Repo) {
	panic("unimplemented")
}

// Removes the repo with the given ID, and its name, from the local cache.
func (r *Repos) dropFromLocal(repoID int64) {
	panic("unimplemented")
}

// Resolves a page URL to its repo and file path, then fetches the cached HTML for that file from {repoID}:docs;
// ErrUnknownRepo for an unknown name, ErrUnknownPath for an unknown route.
func (r *Repos) Doc(ctx context.Context, URL string) (string, error) {
	panic("unimplemented")
}
