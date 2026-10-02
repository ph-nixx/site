package repos

import (
	"context"
	"encoding/json"
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
	rdb   *redis.Client
	cache LocalCache
	mu    sync.RWMutex
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

func New(ctx context.Context, rdb *redis.Client) (*Repos, error) {
	r := Repos{rdb, LocalCache{}, sync.RWMutex{}}
	reposHash, err := r.rdb.HGetAll(ctx, REPOS_KEY).Result()
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

	sub := rdb.Subscribe(ctx, REPOS_UPDATES_CHANNEL)
	if _, err := sub.Receive(ctx); err != nil {
		return nil, err
	}

	go r.sync_local_cache(ctx, sub)
	return &r, nil
}

// Blocks and listens for updates pushed on [REPOS_UPDATES_CHANNEL], and mutates [Repos.cache]
// subject to the message published.
// During local cache reconciliation [sync.RWMutex.Lock] is called and blocks all other goroutines from
// reading or writing to [Repos.cache].
func (r *Repos) sync_local_cache(ctx context.Context, sub *redis.PubSub) {
	defer sub.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-sub.Channel():
			if !ok {
				return
			}
			r.mu.Lock()
			r.mu.Unlock()
		}
	}
}
