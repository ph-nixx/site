package repos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"

	"github.com/google/go-github/v91/github"
	"github.com/redis/go-redis/v9"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// Owns the whole ingestion of one push the handler accepted, from reading the cache to committing the result,
// restarting from the new LastIngestedSHA when another push commits first.
func (r *Repos) Set(ctx context.Context, event *github.PushEvent) error {
	owner, name, after := event.GetRepo().GetOwner().GetLogin(), event.GetRepo().GetName(), event.GetAfter()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var files map[string]File // fetched once: a restart reuses them, since After has not changed
	for restarts := 0; ; restarts++ {
		var cached *Repo
		if restarts == 0 {
			if repos, ok := r.Get(event.GetRepo().GetID()); ok {
				cached = repos[0]
			}
		} else {
			data, err := r.client.HGet(ctx, REPOS_KEY, strconv.FormatInt(event.GetRepo().GetID(), 10)).Bytes()
			if errors.Is(err, redis.Nil) {
				return nil // a deep clean committed first
			}
			if err != nil {
				return err
			}
			if cached, err = newRepo(data); err != nil {
				return err
			}
		}
		var s string
		if cached != nil {
			s = cached.LastIngestedSHA
		}

		var (
			comp    *github.CommitsComparison
			compErr error
			wg      sync.WaitGroup
		)
		if s != "" {
			wg.Go(func() { comp, compErr = r.github.Compare(ctx, owner, name, s, after) })
		}

		if files == nil {
			var err error
			if files, err = r.github.FetchFiles(ctx, owner, name, after, DOCS_CONFIG_PATH, README_PATH); err != nil {
				return err
			}
		}

		// stops that do not depend on the comparison; none of them write, so they are safe before the stale check
		docsToml, readMe := files[DOCS_CONFIG_PATH], files[README_PATH]
		switch {
		case docsToml.Status == FileUnreadable:
			return nil // TODO: log warning
		case docsToml.Status == FileMissing && s == "":
			return nil // not eligible
		case readMe.Status != FileFound && cached == nil:
			return nil // TODO: log warning, README.md is required on the first ingest
		}
		wg.Wait()
		var ghErr *github.ErrorResponse
		if compErr != nil && !(errors.As(compErr, &ghErr) &&
			(ghErr.Response.StatusCode == http.StatusNotFound || ghErr.Response.StatusCode >= http.StatusInternalServerError)) {
			return compErr // TODO: log
		}

		cfg, err := reconcileDocsConfig(docsToml, readMe, event, cached, comp, compErr)
		switch {
		case errors.Is(err, ErrStale):
			return nil
		case errors.Is(err, ErrDocsRemoved):
			_, err = r.dropFromCache(ctx, cached.ID, s) // TODO: handle ok false, another push committed first
			return err
		case errors.Is(err, ErrDocsSyntax):
			return nil // TODO: log warning
		case err != nil:
			return err
		}

		var (
			result              CommitResult
			docsErr, commitsErr error
			processes           sync.WaitGroup
		)
		processes.Go(func() {
			html, err := r.fetchDocs(ctx, cfg)
			if err != nil {
				docsErr = err
				return
			}

			result, docsErr = r.commitDocs(ctx, buildRepo(event, cfg), s, html)
		})
		processes.Go(func() {
			var commits []ConventionalCommit
			if cfg.PushCommits {
				for _, c := range event.Commits {
					if cc, ok := fromPushCommit(event.GetRepo(), c); ok {
						commits = append(commits, cc)
					}
				}
			} else {
				for _, c := range comp.Commits {
					if cc, ok := fromComparisonCommit(event.GetRepo(), c); ok {
						commits = append(commits, cc)
					}
				}
			}

			commitsErr = r.setCommits(ctx, commits)
		})
		processes.Wait()
		if err := errors.Join(docsErr, commitsErr); err != nil {
			return err
		}

		// Committed, or Superseded by a push GitHub received later: either way this push is done
		if result != Overtaken {
			return nil
		}

		if restarts == MAX_RESTARTS {
			return fmt.Errorf("overtaken after %d restarts", MAX_RESTARTS)
		}
	}
}

// Fetches every path in cfg.UpdatePaths at cfg.SHA through one FetchFiles call and renders them, using cfg.ReadMe
// for README.md instead of fetching it again (skipping it when nil, so it keeps its cached HTML), and marks the nodes
// of every missing or unreadable path Missing.
func (r *Repos) fetchDocs(ctx context.Context, cfg *DocsConfig) (map[string]string, error) {
	files, err := r.github.FetchFiles(ctx, cfg.Owner, cfg.Name, cfg.SHA, cfg.UpdatePaths...)
	if err != nil {
		return nil, err
	}

	html := make(map[string]string, len(files)+1)
	if cfg.ReadMe != nil {
		html[README_PATH] = intoHTML(cfg.ReadMe)
	}
	for path, f := range files {
		if f.Status == FileFound {
			html[path] = intoHTML(f.Data)
		}
	}

	// marks Missing every node, at any depth, whose path was fetched but is missing or unreadable
	for stack := [][]DocNode{cfg.Sections}; len(stack) > 0; {
		nodes := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for i := range nodes {
			if f, ok := files[nodes[i].Path]; ok && f.Status != FileFound {
				nodes[i].Missing = true
			}
			stack = append(stack, nodes[i].Children)
		}
	}
	return html, nil
}

// Builds the repo to store at event.After from cfg in docs.toml order: a Missing node with a cached counterpart is frozen
// with its cached segment, title, and path, a Missing node without one is skipped with its whole subtree, and skipped
// subtrees are left out.
func buildRepo(event *github.PushEvent, cfg *DocsConfig) Repo {
	// a node Frozen at parse time or Missing with a cached counterpart takes its cached segment, title, and path, and a
	// Skipped node or one Missing without a cached counterpart is left out with its whole subtree
	var sections func(nodes []DocNode) []DocSection
	sections = func(nodes []DocNode) []DocSection {
		var built []DocSection
		for _, n := range nodes {
			s := DocSection{Key: n.Key, Segment: n.Segment, Title: n.Title, Path: n.Path}
			switch {
			case n.Skipped, n.Missing && n.Cached == nil:
				continue
			case n.Frozen, n.Missing:
				s.Frozen, s.Segment, s.Title, s.Path = true, n.Cached.Segment, n.Cached.Title, n.Cached.Path
			}
			s.SubSections = sections(n.Children) // at most MAX_DOC_DEPTH levels deep: reconcileDocsConfig drops deeper tables
			built = append(built, s)
		}
		return built
	}

	repo := event.GetRepo()
	return Repo{
		ID:              repo.GetID(),
		Name:            repo.GetName(),
		FullName:        repo.GetFullName(),
		Title:           cfg.Title,
		Description:     repo.GetDescription(),
		SVG:             cfg.SVG,
		DocSections:     sections(cfg.Sections),
		LastCommit:      event.GetHeadCommit().GetTimestamp().Time,
		LastIngestedSHA: event.GetAfter(),
		LastPushedAt:    repo.GetPushedAt().Time,
	}
}

// Lists every DocSections path referenced by before that after no longer references; README.md is never old.
// Called by commitDocs with the repo read under WATCH; a nil before (first ingest) has no old paths.
// The old paths come in no particular order, which changes from call to call.
func pruneRepo(before, after *Repo) []string {
	// every non empty DocSections path of r, at any depth; none for a nil r
	paths := func(r *Repo) map[string]bool {
		found := map[string]bool{}
		if r == nil {
			return found
		}
		for stack := [][]DocSection{r.DocSections}; len(stack) > 0; {
			sections := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for i := range sections {
				if sections[i].Path != "" {
					found[sections[i].Path] = true
				}
				stack = append(stack, sections[i].SubSections)
			}
		}
		return found
	}

	kept := paths(after)
	var old []string
	for path := range paths(before) {
		if !kept[path] && path != README_PATH {
			old = append(old, path)
		}
	}
	return old
}

// The GFM renderer behind intoHTML (built once).
var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

// Renders a GitHub Flavored Markdown file to an HTML string.
func intoHTML(md []byte) string {
	var buf bytes.Buffer
	_ = markdown.Convert(md, &buf) // GFM has no invalid input, and writing into a bytes.Buffer never fails
	return buf.String()
}

// How a commit transaction ended.
type CommitResult int

const (
	Committed  CommitResult = iota // the SHA matched and every write was applied
	Superseded                     // a push GitHub received later committed first: stop, this push is stale
	Overtaken                      // a push GitHub received no later committed first: restart from its SHA
)

// Runs the commit transaction; when Repo.LastIngestedSHA no longer equals expected, writes nothing and orders the two
// pushes by LastPushedAt. The old paths are pruned from the stored repo read under WATCH, and only the html of paths
// repo references, plus README.md, is written, so HTML fetched inside a skipped subtree is discarded.
func (r *Repos) commitDocs(ctx context.Context, repo Repo, expected string, html map[string]string) (CommitResult, error) {
	id := strconv.FormatInt(repo.ID, 10)
	data, err := json.Marshal(repo)
	if err != nil {
		return 0, err
	}

	// html for a path repo does not reference was fetched inside a skipped subtree and is never served
	referenced := map[string]bool{README_PATH: true}
	for stack := [][]DocSection{repo.DocSections}; len(stack) > 0; {
		sections := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for i := range sections {
			referenced[sections[i].Path] = true
			stack = append(stack, sections[i].SubSections)
		}
	}
	var pairs []any
	for path, h := range html {
		if referenced[path] {
			pairs = append(pairs, path, h)
		}
	}

	var result CommitResult
	commit := func(tx *redis.Tx) error {
		var stored *Repo // nil when the field is missing, which reads as an empty SHA
		b, err := tx.HGet(ctx, REPOS_KEY, id).Bytes()
		switch {
		case errors.Is(err, redis.Nil):
		case err != nil:
			return err
		default:
			stored = &Repo{}
			if err := json.Unmarshal(b, stored); err != nil {
				return err
			}
		}

		var storedSHA string
		if stored != nil {
			storedSHA = stored.LastIngestedSHA
		}
		if storedSHA != expected {
			// a tie is Overtaken, so the restarted comparison orders the pushes instead of the one second clock
			result = Overtaken
			if stored != nil && stored.LastPushedAt.After(repo.LastPushedAt) {
				result = Superseded
			}
			return nil
		}

		docs := id + DOCS_KEY_SUFFIX
		old := pruneRepo(stored, &repo)
		if _, err := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			if len(old) > 0 {
				pipe.HDel(ctx, docs, old...)
			}
			if len(pairs) > 0 {
				pipe.HSet(ctx, docs, pairs...)
			}
			pipe.HSet(ctx, REPOS_KEY, id, data)
			pipe.Publish(ctx, REPOS_UPDATES_CHANNEL, REPOS_UPDATE_PREFIX+":"+id)
			return nil
		}); err != nil {
			return err
		}
		result = Committed
		return nil
	}

	// WATCH covers the whole repos hash, so a write to any repo aborts EXEC without this repo having changed
	for retries := 0; ; retries++ {
		err := r.client.Watch(ctx, commit, REPOS_KEY)
		if !errors.Is(err, redis.TxFailedErr) {
			return result, err
		}
		if retries == TX_RETRIES {
			return 0, fmt.Errorf("commit aborted by concurrent writes after %d retries", TX_RETRIES)
		}
	}
}

// Runs the deep clean transaction; ok is false when Repo.LastIngestedSHA no longer equals expectedSHA.
func (r *Repos) dropFromCache(ctx context.Context, repoID int64, expectedSHA string) (bool, error) {
	id := strconv.FormatInt(repoID, 10)
	var ok bool
	drop := func(tx *redis.Tx) error {
		var stored Repo // zero when the field is missing, which reads as an empty SHA
		b, err := tx.HGet(ctx, REPOS_KEY, id).Bytes()
		switch {
		case errors.Is(err, redis.Nil):
		case err != nil:
			return err
		default:
			if err := json.Unmarshal(b, &stored); err != nil {
				return err
			}
		}
		if stored.LastIngestedSHA != expectedSHA {
			ok = false // a newer ingest committed first, and a stale push must not wipe it
			return nil
		}

		// the repo's members of commits, matched by repo_id; scanning is linear, which the 90 day prune keeps small
		members, err := tx.ZRange(ctx, COMMITS_UPDATE_PREFIX, 0, -1).Result()
		if err != nil {
			return err
		}
		var owned []any
		for _, m := range members {
			var c ConventionalCommit
			if json.Unmarshal([]byte(m), &c) == nil && c.RepoID == repoID {
				owned = append(owned, m)
			}
		}

		if _, err := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Del(ctx, id+DOCS_KEY_SUFFIX)
			pipe.HDel(ctx, REPOS_KEY, id)
			if len(owned) > 0 {
				pipe.ZRem(ctx, COMMITS_UPDATE_PREFIX, owned...)
			}
			pipe.Publish(ctx, REPOS_UPDATES_CHANNEL, REPOS_UPDATE_PREFIX+":"+id)
			pipe.Publish(ctx, REPOS_UPDATES_CHANNEL, COMMITS_UPDATE_PREFIX)
			return nil
		}); err != nil {
			return err
		}
		ok = true
		return nil
	}

	// WATCH covers the whole repos hash, so a write to any repo aborts EXEC without this repo having changed
	for retries := 0; ; retries++ {
		err := r.client.Watch(ctx, drop, REPOS_KEY)
		if !errors.Is(err, redis.TxFailedErr) {
			return ok, err
		}
		if retries == TX_RETRIES {
			return false, fmt.Errorf("deep clean aborted by concurrent writes after %d retries", TX_RETRIES)
		}
	}
}
