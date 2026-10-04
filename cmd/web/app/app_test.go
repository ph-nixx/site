package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/ph-nixx/site/cmd/web/repos"
	"github.com/redis/go-redis/v9"
)

// stubClient is a repos.Client backed by a fixed hash whose subscription never delivers.
type stubClient struct {
	hash map[string]string
}

func (c stubClient) HGetAll(ctx context.Context, key string) *redis.MapStringStringCmd {
	cmd := redis.NewMapStringStringCmd(ctx, "hgetall", key)
	cmd.SetVal(c.hash)
	return cmd
}

func (c stubClient) HGet(ctx context.Context, key, field string) *redis.StringCmd {
	cmd := redis.NewStringCmd(ctx, "hget", key, field)
	if v, ok := c.hash[field]; ok {
		cmd.SetVal(v)
	} else {
		cmd.SetErr(redis.Nil)
	}
	return cmd
}

func (c stubClient) Subscribe(ctx context.Context, channels ...string) repos.Subscription {
	return stubSub{}
}

type stubSub struct{}

func (stubSub) Channel(opts ...redis.ChannelOption) <-chan *redis.Message { return nil }
func (stubSub) Close() error                                              { return nil }
func (stubSub) Receive(ctx context.Context) (any, error)                  { return nil, nil }

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
	r, err := repos.New(ctx, stubClient{hash}, discard)
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
