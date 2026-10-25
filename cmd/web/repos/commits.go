package repos

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/google/go-github/v91/github"
	"github.com/redis/go-redis/v9"
)

// Parses a comparison commit, timestamped by its committer date.
func fromComparisonCommit(repo *github.PushEventRepository, c *github.RepositoryCommit) (ConventionalCommit, bool) {
	panic("unimplemented")
}

// Parses a push event commit, timestamped by its timestamp, which must equal the comparison's committer date.
func fromPushCommit(repo *github.PushEventRepository, c *github.HeadCommit) (ConventionalCommit, bool) {
	panic("unimplemented")
}

// Builds a ConventionalCommit from a message; ok is false when it does not strictly follow the Conventional Commits spec.
func parseConventionalCommit(id string, repoID int64, timestamp time.Time, message string) (ConventionalCommit, bool) {
	panic("unimplemented")
}

// Adds commits to the commit log and prunes entries older than 90 days.
func (r *Repos) setCommits(ctx context.Context, commits []ConventionalCommit) error {
	members := make([]redis.Z, 0, len(commits))
	for _, c := range commits {
		// a commit logged once from the push event and once from a comparison must serialize to the same member,
		// so the timestamp is normalized to UTC whatever offset the source reported
		c.Timestamp = c.Timestamp.UTC()
		data, err := json.Marshal(c)
		if err != nil {
			return err
		}
		members = append(members, redis.Z{Score: float64(c.Timestamp.Unix()), Member: data})
	}
	var added int64
	if len(members) > 0 {
		var err error
		if added, err = r.client.ZAdd(ctx, COMMITS_UPDATE_PREFIX, members...).Result(); err != nil {
			return err
		}
	}

	cutoff := strconv.FormatInt(time.Now().Add(-COMMIT_RETENTION).Unix(), 10)
	pruned, err := r.client.ZRemRangeByScore(ctx, COMMITS_UPDATE_PREFIX, "-inf", cutoff).Result()
	if err != nil {
		return err
	}

	// re-adding commits already logged is common and changes nothing, so instances reload only on a real change
	if added+pruned == 0 {
		return nil
	}

	return r.client.Publish(ctx, REPOS_UPDATES_CHANNEL, COMMITS_UPDATE_PREFIX).Err()
}
