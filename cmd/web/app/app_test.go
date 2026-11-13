package app

import (
	"context"
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ph-nixx/site/cmd/web/repos"
	"github.com/redis/go-redis/v9"
)

// mockClient is an in-memory repos.Client with a fixed hash and no commits,
// whose subscription never delivers.
type mockClient struct {
	hash    map[string]string
	commits []string // commits JSON, newest first, served by ZRangeArgs
}

func newMockClient(hash map[string]string) *mockClient {
	return &mockClient{hash: hash}
}

func (f *mockClient) HGetAll(ctx context.Context, key string) *redis.MapStringStringCmd {
	cmd := redis.NewMapStringStringCmd(ctx, "hgetall", key)
	cmd.SetVal(f.hash)
	return cmd
}

func (f *mockClient) HGet(ctx context.Context, key, field string) *redis.StringCmd {
	cmd := redis.NewStringCmd(ctx, "hget", key, field)
	if v, ok := f.hash[field]; ok {
		cmd.SetVal(v)
	} else {
		cmd.SetErr(redis.Nil)
	}
	return cmd
}

func (f *mockClient) Subscribe(ctx context.Context, channels ...string) repos.Subscription {
	return &mockSub{}
}

func (f *mockClient) ZRangeArgs(ctx context.Context, z redis.ZRangeArgs) *redis.StringSliceCmd {
	cmd := redis.NewStringSliceCmd(ctx, "zrange", z.Key)
	cmd.SetVal(append([]string{}, f.commits...))
	return cmd
}

func (f *mockClient) Watch(ctx context.Context, fn func(*redis.Tx) error, keys ...string) error {
	panic("unimplemented")
}

func (f *mockClient) ZRange(ctx context.Context, key string, start, stop int64) *redis.StringSliceCmd {
	panic("unimplemented")
}

func (f *mockClient) ZAdd(ctx context.Context, key string, members ...redis.Z) *redis.IntCmd {
	panic("unimplemented")
}

func (f *mockClient) ZRemRangeByScore(ctx context.Context, key, min, max string) *redis.IntCmd {
	panic("unimplemented")
}

func (f *mockClient) Publish(ctx context.Context, channel string, message any) *redis.IntCmd {
	panic("unimplemented")
}

// mockSub is a repos.Subscription that never delivers a message.
type mockSub struct{}

func (s *mockSub) Channel(opts ...redis.ChannelOption) <-chan *redis.Message { return nil }
func (s *mockSub) Close() error                                              { return nil }
func (s *mockSub) Receive(ctx context.Context) (any, error)                  { return nil, nil }

func TestIcon(t *testing.T) {
	const svg = `<svg xmlns="http://www.w3.org/2000/svg"></svg>`
	hash := map[string]string{}
	for _, repo := range []repos.Repo{{ID: 1, SVG: svg}, {ID: 2, SVG: ""}} {
		b, err := json.Marshal(repo)
		if err != nil {
			t.Fatal(err)
		}
		hash[strconv.FormatInt(repo.ID, 10)] = string(b)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	discard := slog.New(slog.DiscardHandler)
	r, err := repos.New(ctx, newMockClient(hash), nil, discard)
	if err != nil {
		t.Fatal(err)
	}
	mux := (&App{Logger: discard, repos: r}).Routes()

	get := func(id, ifNoneMatch string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/static/icons/"+id, nil)
		if ifNoneMatch != "" {
			req.Header.Set("If-None-Match", ifNoneMatch)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// 200: body and caching headers.
	rec := get("1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("known icon: status %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/svg+xml" {
		t.Errorf("Content-Type = %q, want image/svg+xml", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=3600" {
		t.Errorf("Cache-Control = %q, want public, max-age=3600", got)
	}
	etag := rec.Header().Get("ETag")
	if len(etag) < 3 || !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) {
		t.Fatalf("ETag = %q, want a quoted validator", etag)
	}
	if rec.Body.String() != svg {
		t.Errorf("body = %q, want %q", rec.Body.String(), svg)
	}

	// 304: a repeat request with the same validator.
	rec = get("1", etag)
	if rec.Code != http.StatusNotModified {
		t.Errorf("matching If-None-Match: status %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("304 body = %q, want empty", rec.Body.String())
	}
	if got := rec.Header().Get("ETag"); got != etag {
		t.Errorf("304 ETag = %q, want %q", got, etag)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=3600" {
		t.Errorf("304 Cache-Control = %q, want it kept", got)
	}

	// A stale validator gets the full response again.
	if rec = get("1", `"stale"`); rec.Code != http.StatusOK {
		t.Errorf("stale If-None-Match: status %d, want 200", rec.Code)
	}

	// 404: non-numeric, zero, negative, unknown, and empty-SVG IDs.
	for _, id := range []string{"abc", "0", "-1", "99", "2"} {
		if rec := get(id, ""); rec.Code != http.StatusNotFound {
			t.Errorf("id %q: status %d, want 404", id, rec.Code)
		}
	}
}

// The home page's build log lists the newest commits, each with its day, its repo's current name, its header, and a
// link to it on GitHub, and says so when there are none.
func Test_home_renders_the_build_log(t *testing.T) {
	repo := repos.Repo{ID: 1, Name: "my-lib", FullName: "ph-nixx/my-lib"}
	b, err := json.Marshal(repo)
	if err != nil {
		t.Fatal(err)
	}
	commit := func(c repos.ConventionalCommit) string {
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	day := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	tmpl := template.Must(template.New("").Funcs(funcMap).ParseGlob("../../../views/*.html"))
	discard := slog.New(slog.DiscardHandler)

	tests := []struct {
		name    string
		commits []string
		want    []string
	}{
		{"commits", []string{
			commit(repos.ConventionalCommit{ID: "abc123", RepoID: 1, Timestamp: day, Type: "feat", Scope: "docs", Breaking: true, Description: "add the pooling guide"}),
			commit(repos.ConventionalCommit{ID: "def456", RepoID: 2, Timestamp: day, Type: "fix", Description: "of a repo no longer cached"}),
		}, []string{"Oct 25", "my-lib", "feat(docs)!: add the pooling guide", `href="https://github.com/ph-nixx/my-lib/commit/abc123"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newMockClient(map[string]string{"1": string(b)})
			client.commits = tt.commits
			r, err := repos.New(t.Context(), client, nil, discard)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			(&App{Logger: discard, repos: r, tmpl: tmpl}).Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			if rec.Code != http.StatusOK {
				t.Fatal("expected 200, got", rec.Code)
			}
			body := rec.Body.String()
			for _, want := range tt.want {
				if !strings.Contains(body, want) {
					t.Errorf("expected the page to contain %q", want)
				}
			}
			if strings.Contains(body, "of a repo no longer cached") {
				t.Error("expected the commit of an uncached repo left out")
			}
		})
	}
}

// formatDuration rounds to whole hours, never below 1, and switches to whole days once that rounds to 24 or more.
func Test_formatDuration_rounds_to_hours_then_days(t *testing.T) {
	after := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"zero is clamped to an hour", 0, "1h"},
		{"reversed is clamped to an hour", -5 * time.Hour, "1h"},
		{"minutes are clamped to an hour", 10 * time.Minute, "1h"},
		{"rounds down under the half hour", time.Hour + 29*time.Minute, "1h"},
		{"rounds up at the half hour", time.Hour + 30*time.Minute, "2h"},
		{"whole hours", 3 * time.Hour, "3h"},
		{"23 hours stays in hours", 23 * time.Hour, "23h"},
		{"just under 23.5 hours stays in hours", 23*time.Hour + 29*time.Minute, "23h"},
		{"rounding up to 24 hours switches to days", 23*time.Hour + 30*time.Minute, "1d"},
		{"24 hours is a day", 24 * time.Hour, "1d"},
		{"rounds down under the half day", 35*time.Hour + 59*time.Minute, "1d"},
		{"rounds up at the half day", 36 * time.Hour, "2d"},
		{"rounds up to 30 days", 29*24*time.Hour + 12*time.Hour, "30d"},
		{"30 days", 30 * 24 * time.Hour, "30d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatDuration(after, after.Add(tt.d)); got != tt.want {
				t.Errorf("formatDuration over %v = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

// mostActive picks the repo with the most commits in (after, before], breaking ties by the newest commit, and returns
// its stats with the typing choreography for its command, or nil when no commit falls in the interval.
func Test_mostActive_picks_the_busiest_repo_in_the_interval(t *testing.T) {
	before := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	after := before.Add(-TERM_RANGE)
	day := 24 * time.Hour
	hash := map[string]string{}
	for _, repo := range []repos.Repo{{ID: 1, Name: "site", Title: "Site"}, {ID: 2, Name: "pool", Title: "Pool"}} {
		b, err := json.Marshal(repo)
		if err != nil {
			t.Fatal(err)
		}
		hash[strconv.FormatInt(repo.ID, 10)] = string(b)
	}
	commit := func(repoID int64, typ string, at time.Time) repos.ConventionalCommit {
		return repos.ConventionalCommit{ID: typ + at.Format(time.RFC3339), RepoID: repoID, Timestamp: at, Type: typ, Description: "change"}
	}
	discard := slog.New(slog.DiscardHandler)

	tests := []struct {
		name    string
		commits []repos.ConventionalCommit
		want    *termStats
	}{
		{"the interval excludes after and anything past before", []repos.ConventionalCommit{
			commit(1, "feat", before.Add(-3*time.Hour)),
			commit(1, "fix", before.Add(-day)),
			commit(1, "feat", before.Add(-2*day)),
			commit(2, "feat", before.Add(-5*day)),
			// Pool would win if either bound were inclusive the wrong way.
			commit(2, "feat", after),
			commit(2, "fix", after),
			commit(2, "chore", after),
			commit(2, "feat", before.Add(time.Hour)),
		}, &termStats{
			Project: "Site", Name: "site", Range: "30d", Commits: 3, Share: 75, Features: 2, Last: "3h",
			NameChars: 4, FlagChars: 12, FlagsAt: 400, TypedMS: 1000,
		}},
		{"a tie goes to the newest commit", []repos.ConventionalCommit{
			commit(1, "feat", before.Add(-2*day)),
			commit(1, "feat", before.Add(-3*day)),
			commit(2, "fix", before.Add(-time.Hour)),
			commit(2, "feat", before.Add(-10*day)),
		}, &termStats{
			Project: "Pool", Name: "pool", Range: "30d", Commits: 2, Share: 50, Features: 1, Last: "1h",
			NameChars: 4, FlagChars: 12, FlagsAt: 400, TypedMS: 1000,
		}},
		{"a commit at before counts", []repos.ConventionalCommit{
			commit(1, "docs", before),
		}, &termStats{
			Project: "Site", Name: "site", Range: "30d", Commits: 1, Share: 100, Features: 0, Last: "1h",
			NameChars: 4, FlagChars: 12, FlagsAt: 400, TypedMS: 1000,
		}},
		{"no commits in the interval", []repos.ConventionalCommit{
			commit(1, "feat", after.Add(-day)),
			commit(2, "feat", after),
		}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The commit log is served newest first.
			slices.SortFunc(tt.commits, func(a, b repos.ConventionalCommit) int { return b.Timestamp.Compare(a.Timestamp) })
			client := newMockClient(hash)
			for _, c := range tt.commits {
				b, err := json.Marshal(c)
				if err != nil {
					t.Fatal(err)
				}
				client.commits = append(client.commits, string(b))
			}
			r, err := repos.New(t.Context(), client, nil, discard)
			if err != nil {
				t.Fatal(err)
			}

			got := mostActive(r, after, before)
			switch {
			case tt.want == nil && got != nil:
				t.Errorf("mostActive = %+v, want nil", *got)
			case tt.want != nil && got == nil:
				t.Errorf("mostActive = nil, want %+v", *tt.want)
			case tt.want != nil && *got != *tt.want:
				t.Errorf("mostActive = %+v, want %+v", *got, *tt.want)
			}
		})
	}
}
