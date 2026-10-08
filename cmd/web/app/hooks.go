package app

import (
	"net/http"
	"time"
)

// Repository as returned by the GitHub REST API.
type GithubRepo struct {
	ID            int64        `json:"id"`
	Name          string       `json:"name"`
	FullName      string       `json:"full_name"`
	Description   string       `json:"description"`
	Language      string       `json:"language"`
	DefaultBranch string       `json:"default_branch"`
	HeadCommit    GithubCommit `json:"head_commit"`
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

func (a *App) githubPushEvent(w http.ResponseWriter, r *http.Response) {
}
