package repos

import (
	"context"
	"time"

	"github.com/google/go-github/v91/github"
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
	panic("unimplemented")
}
