package repos

import (
	"context"

	"github.com/google/go-github/v91/github"
)

// The GitHub REST API calls ingestion needs.
type Github interface {
	// Fetches a file's contents at a commit through Repositories.GetContents; ok is false when it returns 404.
	FetchFile(ctx context.Context, fullName, path, sha string) (data []byte, ok bool, err error)

	// Compares two commits through Repositories.CompareCommits.
	Compare(ctx context.Context, fullName, base, head string) (*github.CommitsComparison, error)
}

// githubClient adapts *github.Client to Github, splitting FullName into the owner and repo arguments go-github expects.
type githubClient struct {
	*github.Client
}

// Coerces a go-github client as a Github.
func FromGithubClient(gh *github.Client) Github {
	return &githubClient{Client: gh}
}

// Fetches a file's contents at sha; ok is false when GitHub returns 404.
func (g *githubClient) FetchFile(ctx context.Context, fullName, path, sha string) ([]byte, bool, error) {
	panic("unimplemented")
}

// Compares base...head in the repo named by fullName.
func (g *githubClient) Compare(ctx context.Context, fullName, base, head string) (*github.CommitsComparison, error) {
	panic("unimplemented")
}
