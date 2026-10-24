package repos

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/google/go-github/v91/github"
)

// The GitHub API calls ingestion needs: file contents through GraphQL, comparisons through REST.
type Github interface {
	// Fetches the contents of paths at a commit in one GraphQL request; every path in paths has an entry in files.
	FetchFiles(ctx context.Context, owner, repo, sha string, paths ...string) (files map[string]File, err error)

	// Compares two commits through Repositories.CompareCommits (REST).
	Compare(ctx context.Context, owner, repo, base, head string) (*github.CommitsComparison, error)
}

// The outcome of reading one path through FetchFiles.
type FileStatus int

const (
	FileFound      FileStatus = iota // Data holds the file's text
	FileMissing                      // the path does not exist at the commit
	FileUnreadable                   // binary, or too large for GraphQL to return inline
)

// One path read through FetchFiles.
type File struct {
	Status FileStatus
	Data   []byte // nil unless Status is FileFound
}

// githubClient adapts *github.Client to Github, taking the same owner and repo arguments as go-github and the GraphQL
// repository field. GraphQL requests go through the client's NewRequest and Do, so they share its token, error types,
// and rate limit tracking with REST.
type githubClient struct {
	*github.Client
}

// Coerces a go-github client as a Github.
func FromGithubClient(gh *github.Client) Github {
	return &githubClient{Client: gh}
}

// Fetches the contents of paths at sha by POSTing one aliased object(expression:) per path to the graphql endpoint
// through NewRequest and Do; a 200 response with a non empty errors field is a failure.
func (g *githubClient) FetchFiles(ctx context.Context, owner, repo, sha string, paths ...string) (map[string]File, error) {
	files := make(map[string]File, len(paths))
	for batch := range slices.Chunk(paths, GRAPHQL_ALIAS_LIMIT) {
		var q strings.Builder
		fmt.Fprintf(&q, "query { repository(owner: %s, name: %s) {", graphqlString(owner), graphqlString(repo))
		for i, path := range batch {
			fmt.Fprintf(&q, " f%d: object(expression: %s) { ... on Blob { text isBinary isTruncated } }", i, graphqlString(sha+":"+path))
		}
		q.WriteString(" } }")

		req, err := g.NewRequest(ctx, http.MethodPost, "graphql", map[string]string{"query": q.String()})
		if err != nil {
			return nil, err
		}
		var resp struct {
			Data struct {
				Repository map[string]*graphqlBlob `json:"repository"`
			} `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if _, err := g.Do(req, &resp); err != nil {
			return nil, err // a non 200 status, as a *github.ErrorResponse
		}
		if len(resp.Errors) > 0 {
			return nil, fmt.Errorf("graphql: %s", resp.Errors[0].Message)
		}
		for i, path := range batch {
			files[path] = resp.Data.Repository["f"+strconv.Itoa(i)].file()
		}
	}
	return files, nil
}

// One aliased object(expression:) field: null when the path does not exist, and empty when the object is not a blob.
type graphqlBlob struct {
	Text        *string `json:"text"`
	IsBinary    bool    `json:"isBinary"`
	IsTruncated bool    `json:"isTruncated"`
}

// Classifies an aliased field as a File: a null field is missing, and a blob without inline text is unreadable.
func (b *graphqlBlob) file() File {
	switch {
	case b == nil:
		return File{Status: FileMissing}
	case b.Text == nil, b.IsBinary, b.IsTruncated:
		return File{Status: FileUnreadable}
	}
	return File{Status: FileFound, Data: []byte(*b.Text)}
}

// Quotes s as a GraphQL string literal; every escape json.Marshal emits is also a GraphQL string escape.
func graphqlString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// Compares base...head in owner/repo.
func (g *githubClient) Compare(ctx context.Context, owner, repo, base, head string) (*github.CommitsComparison, error) {
	comp, _, err := g.Repositories.CompareCommits(ctx, owner, repo, base, head, nil)
	return comp, err
}
