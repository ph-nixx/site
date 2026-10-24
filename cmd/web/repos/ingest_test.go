package repos

import (
	"cmp"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-github/v91/github"
)

// mockGithub is a Github with canned files; Compare reports how its context ended on compared.
type mockGithub struct {
	files         map[string]File
	fetchErr      error
	compareBlocks bool       // Compare waits for its context to be canceled instead of returning at once
	compared      chan error // receives ctx.Err() when Compare returns
	status        string     // comparison status Compare returns, "ahead" when empty
}

func newMockGithub(files map[string]File, fetchErr error, compareBlocks bool) *mockGithub {
	return &mockGithub{files: files, fetchErr: fetchErr, compareBlocks: compareBlocks, compared: make(chan error, 1)}
}

func (g *mockGithub) FetchFiles(ctx context.Context, owner, repo, sha string, paths ...string) (map[string]File, error) {
	return g.files, g.fetchErr
}

func (g *mockGithub) Compare(ctx context.Context, owner, repo, base, head string) (*github.CommitsComparison, error) {
	if g.compareBlocks {
		<-ctx.Done()
	}
	g.compared <- ctx.Err()
	return &github.CommitsComparison{Status: github.Ptr(cmp.Or(g.status, "ahead"))}, ctx.Err()
}

// Returned in place of the panic of a step that is not implemented yet.
var errUnimplemented = errors.New("reached unimplemented code")

// [Repos.Set] returns without waiting for Compare on every stop that does not depend on the comparison,
// canceling it, and waits for Compare on the cases process zero must classify first.
func Test_set_stops_without_waiting_for_compare_when_the_files_alone_decide(t *testing.T) {
	found := File{Status: FileFound, Data: []byte("x")}
	missing := File{Status: FileMissing}
	unreadable := File{Status: FileUnreadable}
	fetchErr := errors.New("boom")

	tests := []struct {
		name     string
		cached   bool // the repo is cached with a LastIngestedSHA, so Compare runs
		docsToml File
		readMe   File
		fetchErr error
		stops    bool // Set returns without waiting for Compare
	}{
		{"fetch error", true, found, found, fetchErr, true},
		{"docs.toml unreadable", true, unreadable, found, nil, true},
		{"docs.toml unreadable on first ingest", false, unreadable, found, nil, true},
		{"docs.toml missing on first ingest", false, missing, found, nil, true},
		{"README.md missing on first ingest", false, found, missing, nil, true},
		{"README.md unreadable on first ingest", false, found, unreadable, nil, true},
		{"README.md missing once cached", true, found, missing, nil, false},
		{"docs.toml missing once cached", true, missing, found, nil, false},
		{"both files found on first ingest", false, found, found, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gh := newMockGithub(map[string]File{"docs.toml": tt.docsToml, "README.md": tt.readMe}, tt.fetchErr, tt.stops)
			r := Repos{github: gh, repoCache: RepoCache{}}
			if tt.cached {
				r.repoCache[1] = &Repo{ID: 1, LastIngestedSHA: "a"}
			}
			event := &github.PushEvent{
				After: github.Ptr("b"),
				Repo: &github.PushEventRepository{
					ID:    github.Ptr(int64(1)),
					Name:  github.Ptr("one"),
					Owner: &github.User{Login: github.Ptr("owner")},
				},
			}

			done := make(chan error, 1)
			go func() {
				// a case that continues reaches the unimplemented steps past process zero
				defer func() {
					if p := recover(); p != nil {
						if p != "unimplemented" {
							panic(p)
						}
						done <- errUnimplemented
					}
				}()
				done <- r.Set(t.Context(), event)
			}()
			var err error
			select {
			case err = <-done:
			case <-time.After(time.Second):
				t.Fatal("Set waited for a Compare it should have canceled")
			}
			want := tt.fetchErr
			if !tt.stops {
				want = errUnimplemented
			}
			if !errors.Is(err, want) {
				t.Error("expected", want, "got", err)
			}

			if !tt.cached {
				select {
				case <-gh.compared:
					t.Error("expected no Compare on a first ingest")
				default:
				}
				return
			}
			select {
			case compareErr := <-gh.compared:
				if canceled := errors.Is(compareErr, context.Canceled); canceled != tt.stops {
					t.Error("expected Compare canceled", tt.stops, "got", canceled)
				}
			case <-time.After(time.Second):
				t.Fatal("Compare never returned")
			}
		})
	}
}

// [Repos.Set] classifies a diverged comparison by LastPushedAt instead of failing on its status.
func Test_set_does_not_fail_on_a_diverged_comparison(t *testing.T) {
	found := File{Status: FileFound, Data: []byte("x")}
	gh := newMockGithub(map[string]File{"docs.toml": found, "README.md": found}, nil, false)
	gh.status = "diverged"
	r := Repos{github: gh, repoCache: RepoCache{1: {ID: 1, LastIngestedSHA: "a"}}}
	event := &github.PushEvent{
		After: github.Ptr("b"),
		Repo: &github.PushEventRepository{
			ID:    github.Ptr(int64(1)),
			Name:  github.Ptr("one"),
			Owner: &github.User{Login: github.Ptr("owner")},
		},
	}

	if err := r.Set(t.Context(), event); err != nil {
		t.Error("expected no error", "got", err)
	}
}
