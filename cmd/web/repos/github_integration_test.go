//go:build integration

package repos

import (
	"fmt"
	"os"
	"testing"

	"github.com/google/go-github/v91/github"
)

// Reads every named env var, skipping the test when any is unset.
func requireEnv(t *testing.T, names ...string) map[string]string {
	t.Helper()
	env := make(map[string]string, len(names))
	for _, name := range names {
		v, ok := os.LookupEnv(name)
		if !ok {
			t.Skip(name, "is not set")
		}
		env[name] = v
	}
	return env
}

// [githubClient.FetchFiles] classifies every path against the real GraphQL API, splits paths past
// GRAPHQL_ALIAS_LIMIT across queries, and fails on a repo that does not exist.
// GITHUB_TEST_SHA is a commit of GITHUB_TEST_OWNER/GITHUB_TEST_REPO where the three GITHUB_TEST_*_PATH paths are a
// text file, a binary file, and a directory; GITHUB_TOKEN is required since GraphQL rejects unauthenticated requests.
func Test_fetch_files_reads_paths_at_a_commit_through_graphql(t *testing.T) {
	env := requireEnv(t, "GITHUB_TOKEN", "GITHUB_TEST_OWNER", "GITHUB_TEST_REPO", "GITHUB_TEST_SHA",
		"GITHUB_TEST_TEXT_PATH", "GITHUB_TEST_BINARY_PATH", "GITHUB_TEST_DIR_PATH")
	owner, repo, sha := env["GITHUB_TEST_OWNER"], env["GITHUB_TEST_REPO"], env["GITHUB_TEST_SHA"]
	text, missing := env["GITHUB_TEST_TEXT_PATH"], "does-not-exist-"+sha+".md"

	gh, err := github.NewClient(github.WithAuthToken(env["GITHUB_TOKEN"]))
	if err != nil {
		t.Fatal(err)
	}
	g := FromGithubClient(gh)

	t.Run("classifies each path", func(t *testing.T) {
		files, err := g.FetchFiles(t.Context(), owner, repo, sha,
			text, env["GITHUB_TEST_BINARY_PATH"], env["GITHUB_TEST_DIR_PATH"], missing)
		if err != nil {
			t.Fatal(err)
		}
		if f := files[text]; f.Status != FileFound || f.Data == nil {
			t.Error(text, "expected found with its contents", "got", f.Status)
		}
		for path, want := range map[string]FileStatus{
			env["GITHUB_TEST_BINARY_PATH"]: FileUnreadable,
			env["GITHUB_TEST_DIR_PATH"]:    FileUnreadable, // a directory, not a blob
			missing:                        FileMissing,
		} {
			if got := files[path].Status; got != want {
				t.Error(path, "expected", want, "got", got)
			}
		}
	})

	t.Run("splits paths past the alias limit", func(t *testing.T) {
		paths := []string{text}
		for i := range GRAPHQL_ALIAS_LIMIT {
			paths = append(paths, fmt.Sprintf("%s/%d", missing, i))
		}
		files, err := g.FetchFiles(t.Context(), owner, repo, sha, paths...)
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != len(paths) {
			t.Error("expected", len(paths), "files", "got", len(files))
		}
		if files[text].Status != FileFound || files[paths[len(paths)-1]].Status != FileMissing {
			t.Error("expected the first path found and the last, in the second query, missing")
		}
	})

	t.Run("fails on an unknown repo", func(t *testing.T) {
		if _, err := g.FetchFiles(t.Context(), owner, repo+"-does-not-exist", sha, text); err == nil {
			t.Error("expected an error for a repo that does not exist")
		}
	})
}
