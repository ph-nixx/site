package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ph-nixx/site/cmd/web/repos"
)

// pushEventPayload is a push webhook body shaped after GitHub's "webhook-push"
// schema (github/rest-api-description), with every required field present.
const pushEventPayload = `{
  "ref": "refs/heads/main",
  "before": "4e672d7a1f0c3b9e8d2a5c6b7e8f9a0b1c2d3e4f",
  "after": "d5d2788b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f",
  "base_ref": null,
  "created": false,
  "deleted": false,
  "forced": false,
  "compare": "https://github.com/ph-nixx/site/compare/4e672d7a1f0c...d5d2788b3c4d",
  "pusher": {
    "name": "ph-nixx",
    "email": "ph-nixx@users.noreply.github.com"
  },
  "commits": [
    {
      "id": "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0",
      "tree_id": "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c",
      "distinct": true,
      "message": "fix: drop old webhook handler",
      "timestamp": "2026-10-08T13:58:40-07:00",
      "url": "https://github.com/ph-nixx/site/commit/a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0",
      "author": {
        "name": "ph-nixx",
        "email": "ph-nixx@users.noreply.github.com",
        "username": "ph-nixx"
      },
      "committer": {
        "name": "ph-nixx",
        "email": "ph-nixx@users.noreply.github.com",
        "username": "ph-nixx"
      },
      "added": [],
      "removed": ["cmd/web/app/old_hooks.go"],
      "modified": ["cmd/web/app/hooks.go"]
    },
    {
      "id": "d5d2788b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f",
      "tree_id": "9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b",
      "distinct": true,
      "message": "docs\n\nDocument the webhook endpoint.",
      "timestamp": "2026-10-08T14:03:12-07:00",
      "url": "https://github.com/ph-nixx/site/commit/d5d2788b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f",
      "author": {
        "name": "ph-nixx",
        "email": "ph-nixx@users.noreply.github.com",
        "username": "ph-nixx"
      },
      "committer": {
        "name": "GitHub",
        "email": "noreply@github.com",
        "username": "web-flow"
      },
      "added": ["docs/webhooks.md"],
      "removed": [],
      "modified": ["README.md", "cmd/web/app/hooks.go"]
    }
  ],
  "head_commit": {
    "id": "d5d2788b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f",
    "tree_id": "9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b",
    "distinct": true,
    "message": "docs\n\nDocument the webhook endpoint.",
    "timestamp": "2026-10-08T14:03:12-07:00",
    "url": "https://github.com/ph-nixx/site/commit/d5d2788b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f",
    "author": {
      "name": "ph-nixx",
      "email": "ph-nixx@users.noreply.github.com",
      "username": "ph-nixx"
    },
    "committer": {
      "name": "GitHub",
      "email": "noreply@github.com",
      "username": "web-flow"
    },
    "added": ["docs/webhooks.md"],
    "removed": [],
    "modified": ["README.md", "cmd/web/app/hooks.go"]
  },
  "repository": {
    "id": 812345678,
    "node_id": "R_kgDOMG7xTg",
    "name": "site",
    "full_name": "ph-nixx/site",
    "private": false,
    "owner": {
      "name": "ph-nixx",
      "email": "ph-nixx@users.noreply.github.com",
      "login": "ph-nixx",
      "id": 98765432,
      "node_id": "U_kgDOBeMtOA",
      "avatar_url": "https://avatars.githubusercontent.com/u/98765432?v=4",
      "gravatar_id": "",
      "url": "https://api.github.com/users/ph-nixx",
      "html_url": "https://github.com/ph-nixx",
      "type": "User",
      "site_admin": false
    },
    "html_url": "https://github.com/ph-nixx/site",
    "description": "Personal site rendered from Go templates",
    "fork": false,
    "url": "https://api.github.com/repos/ph-nixx/site",
    "forks_url": "https://api.github.com/repos/ph-nixx/site/forks",
    "keys_url": "https://api.github.com/repos/ph-nixx/site/keys{/key_id}",
    "collaborators_url": "https://api.github.com/repos/ph-nixx/site/collaborators{/collaborator}",
    "teams_url": "https://api.github.com/repos/ph-nixx/site/teams",
    "hooks_url": "https://api.github.com/repos/ph-nixx/site/hooks",
    "issue_events_url": "https://api.github.com/repos/ph-nixx/site/issues/events{/number}",
    "events_url": "https://api.github.com/repos/ph-nixx/site/events",
    "assignees_url": "https://api.github.com/repos/ph-nixx/site/assignees{/user}",
    "branches_url": "https://api.github.com/repos/ph-nixx/site/branches{/branch}",
    "tags_url": "https://api.github.com/repos/ph-nixx/site/tags",
    "blobs_url": "https://api.github.com/repos/ph-nixx/site/git/blobs{/sha}",
    "git_tags_url": "https://api.github.com/repos/ph-nixx/site/git/tags{/sha}",
    "git_refs_url": "https://api.github.com/repos/ph-nixx/site/git/refs{/sha}",
    "trees_url": "https://api.github.com/repos/ph-nixx/site/git/trees{/sha}",
    "statuses_url": "https://api.github.com/repos/ph-nixx/site/statuses/{sha}",
    "languages_url": "https://api.github.com/repos/ph-nixx/site/languages",
    "stargazers_url": "https://api.github.com/repos/ph-nixx/site/stargazers",
    "contributors_url": "https://api.github.com/repos/ph-nixx/site/contributors",
    "subscribers_url": "https://api.github.com/repos/ph-nixx/site/subscribers",
    "subscription_url": "https://api.github.com/repos/ph-nixx/site/subscription",
    "commits_url": "https://api.github.com/repos/ph-nixx/site/commits{/sha}",
    "git_commits_url": "https://api.github.com/repos/ph-nixx/site/git/commits{/sha}",
    "comments_url": "https://api.github.com/repos/ph-nixx/site/comments{/number}",
    "issue_comment_url": "https://api.github.com/repos/ph-nixx/site/issues/comments{/number}",
    "contents_url": "https://api.github.com/repos/ph-nixx/site/contents/{+path}",
    "compare_url": "https://api.github.com/repos/ph-nixx/site/compare/{base}...{head}",
    "merges_url": "https://api.github.com/repos/ph-nixx/site/merges",
    "archive_url": "https://api.github.com/repos/ph-nixx/site/{archive_format}{/ref}",
    "downloads_url": "https://api.github.com/repos/ph-nixx/site/downloads",
    "issues_url": "https://api.github.com/repos/ph-nixx/site/issues{/number}",
    "pulls_url": "https://api.github.com/repos/ph-nixx/site/pulls{/number}",
    "milestones_url": "https://api.github.com/repos/ph-nixx/site/milestones{/number}",
    "notifications_url": "https://api.github.com/repos/ph-nixx/site/notifications{?since,all,participating}",
    "labels_url": "https://api.github.com/repos/ph-nixx/site/labels{/name}",
    "releases_url": "https://api.github.com/repos/ph-nixx/site/releases{/id}",
    "deployments_url": "https://api.github.com/repos/ph-nixx/site/deployments",
    "created_at": 1717000000,
    "updated_at": "2026-10-08T21:03:14Z",
    "pushed_at": 1791493392,
    "git_url": "git://github.com/ph-nixx/site.git",
    "ssh_url": "git@github.com:ph-nixx/site.git",
    "clone_url": "https://github.com/ph-nixx/site.git",
    "svn_url": "https://github.com/ph-nixx/site",
    "homepage": null,
    "size": 412,
    "stargazers_count": 3,
    "watchers_count": 3,
    "language": "Go",
    "has_issues": true,
    "has_projects": true,
    "has_downloads": true,
    "has_wiki": false,
    "has_pages": false,
    "has_discussions": false,
    "forks_count": 0,
    "mirror_url": null,
    "archived": false,
    "disabled": false,
    "open_issues_count": 1,
    "license": null,
    "allow_forking": true,
    "is_template": false,
    "web_commit_signoff_required": false,
    "topics": [],
    "visibility": "public",
    "forks": 0,
    "open_issues": 1,
    "watchers": 3,
    "default_branch": "main",
    "stargazers": 3,
    "master_branch": "main"
  },
  "sender": {
    "login": "ph-nixx",
    "id": 98765432,
    "node_id": "U_kgDOBeMtOA",
    "avatar_url": "https://avatars.githubusercontent.com/u/98765432?v=4",
    "gravatar_id": "",
    "url": "https://api.github.com/users/ph-nixx",
    "html_url": "https://github.com/ph-nixx",
    "followers_url": "https://api.github.com/users/ph-nixx/followers",
    "following_url": "https://api.github.com/users/ph-nixx/following{/other_user}",
    "gists_url": "https://api.github.com/users/ph-nixx/gists{/gist_id}",
    "starred_url": "https://api.github.com/users/ph-nixx/starred{/owner}{/repo}",
    "subscriptions_url": "https://api.github.com/users/ph-nixx/subscriptions",
    "organizations_url": "https://api.github.com/users/ph-nixx/orgs",
    "repos_url": "https://api.github.com/users/ph-nixx/repos",
    "events_url": "https://api.github.com/users/ph-nixx/events{/privacy}",
    "received_events_url": "https://api.github.com/users/ph-nixx/received_events",
    "type": "User",
    "site_admin": false
  }
}`

// newHookApp returns an App backed by an empty mock repos client.
func newHookApp(t *testing.T) *App {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	discard := slog.New(slog.DiscardHandler)
	rs, err := repos.New(ctx, newMockClient(map[string]string{}), discard)
	if err != nil {
		t.Fatal(err)
	}
	return &App{Logger: discard, repos: rs}
}

// pushEventResponse wraps body as a push webhook delivery.
func pushEventResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":   {"application/json"},
			"X-Github-Event": {"push"},
		},
		Body: io.NopCloser(strings.NewReader(body)),
	}
}

func Test_github_push_event_is_parsed_correctly(t *testing.T) {
	a := newHookApp(t)
	w := httptest.NewRecorder()

	a.githubPushEvent(w, pushEventResponse(pushEventPayload))

	// The handler hands the event to repos.Set and writes nothing, so GitHub
	// sees an implicit 200 with an empty body.
	if w.Code != http.StatusOK {
		t.Errorf("status %d, want 200", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", w.Body.String())
	}

	// The handler doesn't expose the decoded event, so check parsing by decoding directly.
	var event repos.GithubPushEvent
	if err := json.Unmarshal([]byte(pushEventPayload), &event); err != nil {
		t.Fatal(err)
	}

	ts := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	author := repos.Author{Username: "ph-nixx", Email: "ph-nixx@users.noreply.github.com"}
	wantCommits := []repos.GithubCommit{
		{
			ID:        "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0",
			Timestamp: ts("2026-10-08T13:58:40-07:00"),
			Author:    author,
			Distinct:  true,
			Message:   "fix: drop old webhook handler",
			Added:     []string{},
			Modified:  []string{"cmd/web/app/hooks.go"},
			Removed:   []string{"cmd/web/app/old_hooks.go"},
		},
		{
			ID:        "d5d2788b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f",
			Timestamp: ts("2026-10-08T14:03:12-07:00"),
			Author:    author,
			Distinct:  true,
			Message:   "docs\n\nDocument the webhook endpoint.",
			Added:     []string{"docs/webhooks.md"},
			Modified:  []string{"README.md", "cmd/web/app/hooks.go"},
			Removed:   []string{},
		},
	}

	wantRepo := repos.GithubRepo{
		ID:          812345678,
		Name:        "site",
		FullName:    "ph-nixx/site",
		Description: "Personal site rendered from Go templates",
		Language:    "Go",
	}

	// Compare marshaled bytes: time.Time values parsed separately don't share a
	// *Location, so reflect.DeepEqual would report a false mismatch.
	for name, pair := range map[string][2]any{
		"repository":  {event.Repo, wantRepo},
		"commits":     {event.Commits, wantCommits},
		"head_commit": {event.HeadCommit, wantCommits[len(wantCommits)-1]},
	} {
		got, err := json.Marshal(pair[0])
		if err != nil {
			t.Fatal(err)
		}
		want, err := json.Marshal(pair[1])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s mismatch\n got: %s\nwant: %s", name, got, want)
		}
	}
}

func Test_github_push_event_with_invalid_json_returns_500(t *testing.T) {
	for name, body := range map[string]string{
		"empty":     "",
		"malformed": `{"repository": {"id": 1,`,
		"not json":  "ref=refs/heads/main",
		"bad type":  `{"repository": {"id": "812345678"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			a := newHookApp(t)
			w := httptest.NewRecorder()

			a.githubPushEvent(w, pushEventResponse(body))

			if w.Code != http.StatusInternalServerError {
				t.Errorf("status %d, want 500", w.Code)
			}
		})
	}
}
