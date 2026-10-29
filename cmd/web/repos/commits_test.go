package repos

import (
	"reflect"
	"testing"
	"time"

	"github.com/google/go-github/v91/github"
)

// [parseConventionalCommit] keeps every message that follows the rules in the RFC's Parsing commit messages, with its
// type, scope, breaking marker, and body, and drops every message that breaks one.
func Test_parse_conventional_commit_follows_the_parsing_rules(t *testing.T) {
	at := time.Unix(1_000_000, 0).UTC()
	// every kept message needs a body; the header cases share this one
	const why = "\n\nwhy"
	commit := func(c ConventionalCommit) ConventionalCommit {
		c.ID, c.RepoID, c.Timestamp = "abc", 7, at
		if c.Body == "" {
			c.Body = "why"
		}
		return c
	}

	kept := []struct {
		name    string
		message string
		want    ConventionalCommit
	}{
		{"header and body", "feat: add search" + why, commit(ConventionalCommit{Type: "feat", Description: "add search"})},
		{"trailing newline", "feat: add search" + why + "\n", commit(ConventionalCommit{Type: "feat", Description: "add search"})},
		{"any type", "chore: bump deps" + why, commit(ConventionalCommit{Type: "chore", Description: "bump deps"})},
		{"scope", "fix(parser): handle tabs" + why, commit(ConventionalCommit{Type: "fix", Scope: "parser", Description: "handle tabs"})},
		{"type is lowercased, scope kept as written", "FEAT(API): add search" + why,
			commit(ConventionalCommit{Type: "feat", Scope: "API", Description: "add search"})},
		{"! marks breaking", "feat!: drop node 6" + why, commit(ConventionalCommit{Type: "feat", Breaking: true, Description: "drop node 6"})},
		{"scope and !", "feat(api)!: drop v1" + why,
			commit(ConventionalCommit{Type: "feat", Scope: "api", Breaking: true, Description: "drop v1"})},
		{"squash merge", "feat: add search (#12)" + why, commit(ConventionalCommit{Type: "feat", Description: "add search (#12)"})},
		{"body of several paragraphs", "fix: x\n\nfirst paragraph\nstill first\n\nsecond paragraph",
			commit(ConventionalCommit{Type: "fix", Description: "x", Body: "first paragraph\nstill first\n\nsecond paragraph"})},
		{"footer lines are body text", "fix: x\n\nwhy it changed\n\nRefs: #123\nCo-Authored-By: Claude <noreply@anthropic.com>",
			commit(ConventionalCommit{Type: "fix", Description: "x", Body: "why it changed\n\nRefs: #123\nCo-Authored-By: Claude <noreply@anthropic.com>"})},
		{"a footer line alone is a body", "fix: x\n\nRefs: #1", commit(ConventionalCommit{Type: "fix", Description: "x", Body: "Refs: #1"})},
		{"BREAKING CHANGE line does not mark breaking", "feat: x\n\nBREAKING CHANGE: y",
			commit(ConventionalCommit{Type: "feat", Description: "x", Body: "BREAKING CHANGE: y"})},
		{"CRLF line endings", "fix: x\r\n\r\nwhy\r\n\r\nRefs: #1",
			commit(ConventionalCommit{Type: "fix", Description: "x", Body: "why\n\nRefs: #1"})},
	}
	for _, tt := range kept {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseConventionalCommit("abc", 7, at, tt.message)
			if !ok {
				t.Fatal("expected the message kept")
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("expected %+v got %+v", tt.want, got)
			}
		})
	}

	dropped := []struct{ name, message string }{
		{"empty message", ""},
		{"header only", "feat: add search"},
		{"header with a trailing newline", "feat: add search\n"},
		{"blank body", "feat: add search\n\n \n\n"},
		{"no separator", "add search"},
		{"space before the colon", "feat : add search"},
		{"no space after the colon", "feat:add search"},
		{"two spaces after the colon", "feat:  add search"},
		{"empty description", "feat: "},
		{"empty scope", "feat(): add search"},
		{"parenthesis inside the scope", "feat(a(b)): add search"},
		{"digit in the type", "feat2: add search"},
		{"! after the colon", "feat:! add search"},
		{"GitHub merge commit", "Merge pull request #12 from user/branch"},
		{"git revert", "Revert \"feat: add search\""},
		{"no blank line before the body", "feat: add search\nbody right below"},
	}
	for _, tt := range dropped {
		t.Run(tt.name, func(t *testing.T) {
			if got, ok := parseConventionalCommit("abc", 7, at, tt.message); ok {
				t.Errorf("expected the message dropped, got %+v", got)
			}
		})
	}
}

// [fromComparisonCommit] and [fromPushCommit] build the same commit from the two sources GitHub reports it in, so it
// serializes to one member of the commit log whichever source a push used.
func Test_comparison_and_push_commits_parse_to_the_same_commit(t *testing.T) {
	repo := &github.PushEventRepository{ID: github.Ptr(int64(7))}
	message := "feat(api)!: drop v1\n\nBREAKING CHANGE: v1 routes are gone"
	committed := time.Unix(1_000_000, 0)

	fromComparison, ok := fromComparisonCommit(repo, &github.RepositoryCommit{
		SHA: github.Ptr("abc"),
		Commit: &github.Commit{
			Message:   github.Ptr(message),
			Committer: &github.CommitAuthor{Date: &github.Timestamp{Time: committed.UTC()}},
		},
	})
	if !ok {
		t.Fatal("expected the comparison commit kept")
	}
	fromPush, ok := fromPushCommit(repo, &github.HeadCommit{
		ID:        github.Ptr("abc"),
		Message:   github.Ptr(message),
		Timestamp: &github.Timestamp{Time: committed.In(time.FixedZone("CST", -6*3600))},
	})
	if !ok {
		t.Fatal("expected the push commit kept")
	}

	if !fromComparison.Timestamp.Equal(fromPush.Timestamp) {
		t.Error("expected the same instant, got", fromComparison.Timestamp, fromPush.Timestamp)
	}
	// commitDocs stores Timestamp in UTC, so only the instant has to match
	fromComparison.Timestamp, fromPush.Timestamp = time.Time{}, time.Time{}
	if !reflect.DeepEqual(fromComparison, fromPush) {
		t.Errorf("expected the same commit, got %+v and %+v", fromComparison, fromPush)
	}
	if fromPush.ID != "abc" || fromPush.RepoID != 7 || !fromPush.Breaking || fromPush.Scope != "api" {
		t.Errorf("unexpected commit %+v", fromPush)
	}
}
