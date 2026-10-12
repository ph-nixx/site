---
feature_name: github_doc_parsing_and_caching
start_date: 2026-10-09
status: open
---

## Summary
[summary]: #summary

When a GitHub push event to the default branch arrives for an eligible repo, the server brings its Redis cache in line
with the repo's documentation at the pushed commit (the event's `after` SHA) and with its commit history.
Every file read for a push uses that same `after` SHA.
The work is split into an initial sequential step that validates the push, followed by two phases that run concurrently
and write independently once validation passes.

**Initial step.** Make two GitHub API requests in parallel:

* Fetch `docs.toml` from the repo root at `after`.
* Compare `Repo.LastIngestedSHA...after`, where `Repo.LastIngestedSHA` is the last commit whose docs were fully ingested.

The comparison's `status` decides what happens next: `ahead` continues; `behind` or `identical` means a newer or
identical push was already ingested, so the push stops without writing anything; `diverged` (a force push) triggers a
full reingest of every referenced path, as does an empty `Repo.LastIngestedSHA` (first ingest), a 5xx from the
comparison, or a truncated file list (300 files).
Comparing from what was actually ingested, rather than trusting the push's own commits, means a missed, failed, or
out of order push is picked up by the next one.

Once both requests return, the push is validated before either phase starts:

* the repo name must not be on the server's hard coded blacklist of names reserved for site routes;
* `docs.toml` must parse;
* the project file must exist.

`docs.toml` is the single source of truth for the repo's docs structure; it is parsed into `[]DocSection`,
a recursive type (`DocSection.SubSections []DocSection`) at most three levels deep (chapter, section, subsection).
A repo is eligible only if `docs.toml` exists at the repo root (it may be empty) and its project file exists.
The project file is `[project].path` when it is set, otherwise `README.md`; an explicitly set `[project].path` never falls back to `README.md`.
Every `path` in `docs.toml` is relative to the repo root, so doc files can live anywhere in the repo.

The project file needs a GitHub request only on the first ingest, after a `diverged` comparison, or when its path differs
from the cached one; otherwise the comparison answers the question for free, since a path that existed at the last ingest
still exists unless the comparison lists it as removed (or as the old name of a rename).
If validation fails, the push logs a warning and stops: nothing is written and `Repo.LastIngestedSHA` is unchanged,
so the repo's cached docs and commits stay frozen until a later push fixes the problem and catches up.
The one exception is a `docs.toml` that existed at the last ingest and now returns 404: that triggers a deep clean,
which removes every trace of the repo from the cache, including its `repos` entry, its `{Repo.ID}:docs` hash,
and its members of `commits` (matched by `ConventionalCommit.RepoID`).
A repo whose `docs.toml` is later restored is ingested from scratch.

**Redis layout.**

* `repos`: a hash mapping each `Repo.ID` to its `Repo` JSON string, including `Repo.DocSections`.
  Keeping metadata in one hash makes fetching every repo for the landing page a single `HGETALL`.
* `{Repo.ID}:docs`: a hash mapping each ingested Markdown file's path, relative to the repo root
  (e.g. `README.md`, `docs/tutorials/database/connect.md`), to its rendered HTML string.
  One hash per repo gives each repo its own path namespace and makes removing a repo's docs a single `DEL`.
  Field keys match the file paths GitHub reports in comparisons, so no translation is needed.
* `commits`: a sorted set of `ConventionalCommit` JSON strings scored by commit timestamp, aggregated across all repos.

**Phase one: docs.** Compare the cached `Repo.DocSections` with the freshly parsed one, together with the comparison's
file list, to classify every referenced Markdown path:

* **new**: referenced now but not before; fetch, parse to HTML, `HSET`.
* **outdated**: referenced before and now, and changed in the comparison; fetch, parse to HTML, `HSET`.
* **old**: referenced before but not now; `HDEL`.
* **unchanged**: everything else; no work.

A chapter or section that becomes invalid, for example because its `path` points to a file that does not exist,
is frozen: the cached node and its whole subtree, with their cached HTML, are kept exactly as they were at the last ingest
until a later push makes the node valid again. At that point the frozen subtree is reconciled with the new one:
tables added, removed, or changed in `docs.toml` while it was frozen take effect, and every file in the new subtree is
refetched, since edits made during the freeze are not in the comparison.
An invalid node that was never ingested has nothing to freeze, so it is skipped.
Cached pages are removed only when their table is removed from `docs.toml`, which removes the table's descendants
recursively and puts their paths in **old**.
Existence is known without extra requests: **new** and **outdated** paths are fetched anyway, where a 404 marks them
invalid, and an **unchanged** path still exists unless the comparison lists it as removed.
A chapter's `path` is optional; a chapter without one is a non-clickable heading and contributes no path.
Fetches for **new** and **outdated** paths run concurrently.
All writes for the push, including the `Repo` JSON string with `Repo.LastIngestedSHA` set to `after`, are applied by a
single Lua script, and only if the stored `Repo.LastIngestedSHA` still equals the value read at the start.
The `Repo` JSON string is overwritten on every successful push, since one `HSET` is cheaper than diffing the metadata.
If the check fails, another push committed first, so the push restarts from the new `Repo.LastIngestedSHA`
until it commits or the comparison reports `behind` or `identical`.
Any other failure writes nothing and leaves `Repo.LastIngestedSHA` unchanged, so the next push retries the work.

**Phase two: commits.** Parse the comparison's commits into `ConventionalCommit` values, dropping any commit whose
message does not strictly follow the Conventional Commits specification, add them to `commits`,
then prune every member whose score is older than the current UTC time minus 90 days.
When there is no usable comparison (first ingest or `diverged`), the push event's own commits are used instead.
Using the comparison means commits from pushes that were frozen or failed are logged once a later push succeeds;
commits that were already logged are added again harmlessly, since identical members are not duplicated.
Phase two does not wait for phase one's write: once validation passes, a failure in phase one (a rate limit, a timeout,
or a lost Lua check) delays only the docs, which the next push catches up.

## Guide-level explanation
[guide-level-explanation]: #guide-level-explanation

Any repo can publish documentation to the site by adding a `docs.toml` file to the repo root.
Once that file exists, every push to the default branch keeps the site's copy of the docs up to date; there is no build step or deploy to run.

### Concepts

* **Project page**: the repo's landing page in the docs, rendered from `README.md` unless `[project].path` says otherwise.
  It is served at `/{repo name}`, where the repo name always matches the GitHub repo name and cannot be configured;
  `[project].title` only changes the displayed name.
* **Chapter**: a top level table in `docs.toml`, shown as a top level heading in the sidebar.
  A chapter may have its own page, without one it's a plain heading that only groups its sections.
* **Section**: a table nested under a chapter (or under another section), always backed by a Markdown file.
  Sections nest at most two levels below a chapter.
* **Segment**: the one piece of a page's URL path that a chapter or section contributes, defaulting to its table name.
  A table only names its own segment; its full path is its ancestors' segments stacked in front of it,
  so a child always lives under its parent's path.
* **Title**: the text shown in the sidebar, defaulting to the table name with dashes turned into spaces
  and the first character capitalized.

Every table name must be a slug, e.g. `getting-started`, and `[project]` is reserved for the project page.

### Example

```
my-lib/
├── README.md
├── docs.toml
└── docs/
    ├── getting-started.md
    ├── install/
    │   ├── linux.md
    │   └── macos.md
    └── guides/
        ├── database.md
        └── database-pooling.md
```

```toml
# docs.toml

[project]
title = "My Lib"

[getting-started]
path = "docs/getting-started.md"

[install]
title = "Installation"

[install.linux]
path = "docs/install/linux.md"

[install.macos]
path = "docs/install/macos.md"
title = "macOS"

[guides]

[guides.database]
path = "docs/guides/database.md"

[guides.database.pooling]
path = "docs/guides/database-pooling.md"
```

| Sidebar                         | URL                               | File                              |
| ------------------------------- | --------------------------------- | --------------------------------- |
| My Lib                          | `/my-lib`                         | `README.md`                       |
| Getting started                 | `/my-lib/getting-started`         | `docs/getting-started.md`         |
| Installation                    | (heading only)                    |                                   |
| &nbsp;&nbsp;Linux               | `/my-lib/install/linux`           | `docs/install/linux.md`           |
| &nbsp;&nbsp;macOS               | `/my-lib/install/macos`           | `docs/install/macos.md`           |
| Guides                          | (heading only)                    |                                   |
| &nbsp;&nbsp;Database            | `/my-lib/guides/database`         | `docs/guides/database.md`         |
| &nbsp;&nbsp;&nbsp;&nbsp;Pooling | `/my-lib/guides/database/pooling` | `docs/guides/database-pooling.md` |

Every page URL follows `/{repo name}/{chapter}/{section}/{sub section}`.
Each URL path is built by stacking segments: `pooling` sits under `database`, which sits under `guides`.
A heading-only chapter has no page of its own, but its segment still prefixes its children.
Setting `segment = "mac"` on `[install.macos]` would change only that page's URL to `/my-lib/install/mac`
setting `segment = "setup"` on `[install]` would move both children, to `/my-lib/setup/linux` and `/my-lib/setup/macos`.
URL paths and file paths are independent: renaming a segment moves a page's URL without touching its file.
When a page is requested, the server maps the repo name to its GitHub repo ID and the segments to the page's file path,
then serves the HTML cached for that file.

Every `path` in `docs.toml` is relative to the repo root, so it is exactly the path GitHub shows for the file.
Keeping doc files under `docs/` is a convention, not a requirement.

### What happens when you push

The site only does work for what a push actually changed. Continuing the example:

* You edit `docs/install/linux.md`. Only the Linux page is fetched and re-rendered; every other page is left alone.
* You change `title = "macOS"` to `title = "MacOS"`. The sidebar updates, but no Markdown is fetched,
  because no referenced file changed.
* You add `[guides.caching]` pointing to a new `docs/guides/caching.md`. That one file is fetched and rendered. 
* You delete the `[guides.database]` table. Both the Database and Pooling pages disappear,
  since removing a table removes everything nested under it. Removing a table is the only way to remove a page.
* You set `path = "docs/install/mac.md"` by mistake. That file does not exist, so the macOS entry is frozen:
  the site keeps serving the macOS page (and anything nested under it) exactly as it was at the last successful push,
  while the rest of the docs update normally. Once the path points to a real file again, the macOS entry catches up
  with everything that changed while it was frozen: entries added or removed under it, renamed segments and titles,
  and edited pages. The same applies to a chapter: if `[install]` set a `path` that does not exist, the whole
  Installation chapter would be frozen.
* You add `[guides.caching]` but forget to commit `docs/guides/caching.md`. There is no earlier version to keep,
  so the entry is skipped until the file exists.
* You set `segment = "linux"` on `[install.macos]`. Two siblings now share a segment, so both are invalid:
  the Linux and macOS entries (and anything nested under them) stay frozen at their last ingested versions
  until the collision is fixed.
* You add `[guides.database.pooling.tuning]`. It is ignored, because it would be a fourth level of nesting.
* You set `[project] path = "OVERVIEW.md"` but the file does not exist. The push fails early with a logged warning,
  even though `README.md` exists; the fallback to `README.md` only applies when `[project].path` is not set.
  The site keeps serving the docs from the last successful push until you fix the mistake.
* You push a `docs.toml` with a syntax error. Same as above: the push fails early, the cached docs stay as they were,
  and the next push with a valid `docs.toml` picks up every change made in the meantime.
* You delete `docs.toml`. This is the only way to remove a repo's docs: a deep clean removes every trace of the repo
  from the site's cache, including its project page and its entries in the commit log.
* You add `docs.toml` back. The repo is ingested again from scratch, as if it were new.

Every push also adds its commits to the site wide commit log, which shows the last 90 days of activity across all repos.

### How to think about it

Treat `docs.toml` as the table of contents and the Markdown files as the pages.
The site mirrors whatever the table of contents on the default branch says, and re-renders a page only when
it is new to the table of contents or a commit touched its file.
A mistake in one entry freezes that entry and what is nested under it at its last good version, and never affects
the rest of the docs; only removing a table removes pages.

## Reference-level explanation
[reference-level-explanation]: #reference-level-explanation

This section follows a push from the webhook to Redis, then a page request from the browser back to Redis,
and ends by revisiting the Guide-level examples.

### `docs.toml` schema

`docs.toml` lives at the repo root.
It must exist for a repo to be eligible, but it may be empty, in which case every value takes its default
and the repo gets a project page and no chapters.

```toml
[project] # optional; reserved table name
title = "Name" # optional, defaults to the repo name with the first character capitalized
path = "x/y.md" # optional, relative to the repo root, must end with .md, defaults to README.md
svg = '<svg></svg>' # optional

[project-chapter-beta] # optional; table name must be a slug
path = "x/y/z.md" # optional, relative to the repo root, must end with .md; without it the chapter is a heading only
segment = "a" # optional, must be a slug, defaults to the table name e.g. "project-chapter-beta"
title = "Project Chapter Beta" # optional, defaults to the table name with dashes turned into spaces and the first character capitalized e.g. "Project chapter beta"

[project-chapter-beta.section-a] # optional; table name must be a slug
path = "x/y/z.md" # required, relative to the repo root, must end with .md
segment = "b" # optional, must be a slug, defaults to the table name e.g. "section-a"
title = "Section A (updated)" # optional, defaults to the table name with dashes turned into spaces and the first character capitalized e.g. "Section a"

# nesting can only go up to a depth of 3; deeper tables are ignored
[project-chapter-beta.section-a.section-b] # optional; table name must be a slug
path = "x/y/z/w.md" # required, relative to the repo root, must end with .md
segment = "c" # optional, must be a slug, defaults to the table name e.g. "section-b"
title = "Section B" # optional, defaults to the table name with dashes turned into spaces and the first character capitalized e.g. "Section b"
```

A slug matches `^[a-z0-9]+(-[a-z0-9]+)*$`.
Depth is counted from the chapter: chapter is 1, section is 2, subsection is 3.

Validation happens at two levels.
**File level** errors fail the whole push early (see [Validation](#validation)):

* `docs.toml` is not valid TOML.
* an explicitly set `[project].path` does not exist, or `[project].path` is unset and `README.md` does not exist.

**Node level** errors make one chapter or section invalid. A warning is logged, the rest of the tree is ingested
normally, and the invalid node is [[0001-github-doc-parsing-and-caching#Node identity and frozen nodes|frozen]] if it was ingested before, or skipped if it
is new:

* The table name or `segment` is not a slug.
* A section has no `path`, or any `path` does not end in `.md`.
* The `path` points to a file that does not exist at `after`.
* Two siblings resolve to the same segment (both siblings are invalid).

Tables deeper than depth 3 are ignored with a warning.

### Node identity and frozen nodes

A node is identified by its table key path, the chain of table names from its chapter down (e.g. `guides.database`),
independent of its `segment`, `title`, and `path`.
Renaming a table therefore removes one node and adds another.

* **Removed**: a cached node whose key path no longer appears in `docs.toml` (including a table now nested deeper than
  depth 3) is removed together with all of its descendants, and their paths fall into **old**.
  This is the only way a cached page is removed.
* **Frozen**: an invalid node whose key path exists in the cached tree is replaced by its cached counterpart, with the
  cached counterpart's entire subtree, marked `Frozen`. Every path in that subtree keeps its cached HTML, and anything
  `docs.toml` now says about the node or its descendants is ignored while it stays frozen. A frozen node is served
  like any other.
* **Skipped**: an invalid node with no cached counterpart has nothing to freeze, so it and its subtree are left out.
* **Unfrozen**: a node that is valid now but was `Frozen` at the last ingest is reconciled like any other node:
  its new subtree from `docs.toml` replaces the frozen one, so descendants added while it was frozen are **new**,
  descendants removed while it was frozen fall into **old**, and changed segments, titles, and paths take effect.
  In addition, every path in the new subtree is treated as **outdated**. Other pushes kept succeeding while it was
  frozen, so `Repo.LastIngestedSHA` has moved past any edits made to its files, and the comparison alone would miss them.

### Data model

`Repo` gains the fields that describe the docs; `ConventionalCommit` gains `RepoID` so a deep clean can find a repo's
commits even after the repo is renamed.

```go
// A Git repo displayed as a project, stored as a JSON string in the `repos` hash.
type Repo struct {
	ID              int64        `json:"id"`                // immutable GitHub repo ID
	Name            string       `json:"name"`              // GitHub repo name, the first URL segment
	FullName        string       `json:"full_name"`         // owner/name, used for GitHub API requests
	Title           string       `json:"title"`             // [project].title, or Name with the first character capitalized
	Description     string       `json:"description"`       // GitHub repo description
	SVG             string       `json:"svg"`               // [project].svg
	ProjectPath     string       `json:"project_path"`      // [project].path, or README.md
	DocSections     []DocSection `json:"doc_sections"`      // chapters as served: invalid nodes frozen or skipped
	LastCommit      time.Time    `json:"last_commit"`       // timestamp of the push's head commit
	LastIngestedSHA string       `json:"last_ingested_sha"` // last commit whose docs were fully ingested
}

// A chapter, section, or subsection parsed from docs.toml.
type DocSection struct {
	Table       string       `json:"table"`            // table name; the chain of table names is the node's identity
	Frozen      bool         `json:"frozen,omitempty"` // kept from the last ingest because the node is currently invalid
	Segment     string       `json:"segment"`          // this node's URL segment, without its ancestors
	Title       string       `json:"title"`            // sidebar text
	Path        string       `json:"path"`             // repo root relative file path; empty for a heading only chapter
	SubSections []DocSection `json:"sub_sections"`     // children, at most depth 3 in total
}

// A Git commit that conforms to the Conventional Commits standard.
type ConventionalCommit struct {
	ID        string    `json:"id"`
	RepoID    int64     `json:"repo_id"` // new: matches Repo.ID, used by the deep clean
	RepoName  string    `json:"repo_name"`
	Timestamp time.Time `json:"timestamp"`

	Type        string `json:"type"`
	Scope       string `json:"scope"`
	Description string `json:"description"`
	Body        string `json:"body"`
}
```

The push event types gain the fields the ingestion reads:

```go
type GithubPushEvent struct {
	Ref        string         `json:"ref"`   // new: e.g. refs/heads/main
	After      string         `json:"after"` // new: the SHA the branch now points to
	Repo       GithubRepo     `json:"repository"`
	HeadCommit GithubCommit   `json:"head_commit"`
	Commits    []GithubCommit `json:"commits"`
}

type GithubRepo struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Description   string `json:"description"`
	Language      string `json:"language"`
	DefaultBranch string `json:"default_branch"` // new
}
```

And the subset of the compare API response that is used:

```go
// Response of GET /repos/{owner}/{repo}/compare/{base}...{head}.
type GithubComparison struct {
	Status  string                   `json:"status"` // ahead, behind, identical, or diverged
	Commits []GithubComparisonCommit `json:"commits"`
	Files   []GithubComparisonFile   `json:"files"`
}

type GithubComparisonCommit struct {
	SHA    string             `json:"sha"`
	Commit GithubCommitDetail `json:"commit"`
}

type GithubCommitDetail struct {
	Message   string             `json:"message"`
	Committer GithubCommitPerson `json:"committer"`
}

type GithubCommitPerson struct {
	Name  string    `json:"name"`
	Email string    `json:"email"`
	Date  time.Time `json:"date"`
}

type GithubComparisonFile struct {
	Filename         string `json:"filename"`
	Status           string `json:"status"` // added, removed, modified, renamed, copied, changed, unchanged
	PreviousFilename string `json:"previous_filename"`
}
```

Intermediate values passed between steps:

```go
// The project settings and section tree read from docs.toml.
type DocsConfig struct {
	Title          string
	SVG            string
	ProjectPath    string
	ProjectPathSet bool                // true when [project].path was written explicitly, so README.md is never a fallback
	Sections       []DocSection        // every table up to depth 3, valid or not
	Invalid        map[string]struct{} // key paths of nodes with a parse time node level error
}

// The file changes between Repo.LastIngestedSHA and after, as reported by the comparison.
type ChangedFiles struct {
	Full    bool                // true when the comparison cannot be trusted and every referenced path is refetched
	Touched map[string]struct{} // added, modified, changed, copied, and the new name of a rename
	Removed map[string]struct{} // removed, and the old name of a rename
}

// The classification of every referenced path for one push.
type PathSets struct {
	New      []string
	Outdated []string
	Old      []string
}
```

### Redis layout

| Key | Type | Contents |
|---|---|---|
| `repos` | hash | `Repo.ID` to `Repo` JSON string |
| `{Repo.ID}:docs` | hash | repo root relative file path to rendered HTML string |
| `commits` | sorted set | `ConventionalCommit` JSON strings scored by `Timestamp` as Unix seconds |
| `repos:updates` | pub/sub channel | `repos:{Repo.ID}` after a docs write or deep clean, `commits` after a commit log write |

A commit is logged at most once because the same commit always serializes to the same JSON string:
every `ConventionalCommit` field is derived from the commit itself, and `Timestamp` is always the committer date,
whether it comes from the comparison or from the push event.
No field may ever be derived from the push (such as the time it was received), or the same commit would become two members.

### Webhook

```go
// Responds 202 to a GitHub push webhook and starts ingestion in the background.
func (a *App) githubPushEvent(w http.ResponseWriter, r *http.Request)
```

1. Decode the body into `GithubPushEvent`; respond 400 if it does not decode.
2. Respond 202 immediately.
3. If `Ref` is not `refs/heads/{event.Repo.DefaultBranch}`, stop.
4. Run `Repos.Set` in a goroutine with its own context and a 60 second timeout, independent of the request.

If an instance shuts down mid ingestion the work is lost, but `Repo.LastIngestedSHA` has not moved, so the next push
redoes it.

### Ingestion

```go
// Brings the cache in line with the repo at event.After, retrying when another push commits first.
func (r *Repos) Set(ctx context.Context, event GithubPushEvent) error
```

#### Initial step

1. If `event.Repo.Name` is on the hard coded blacklist, log and stop.
2. Read the cached `Repo` for `event.Repo.ID` from the in memory cache; `S` is its `LastIngestedSHA`, or empty if the
   repo is not cached.
3. In parallel fetch `docs.toml` at `event.After`, if `S` is not empty, compare `S...event.After`.
4. Turn the comparison into `ChangedFiles`:

| Comparison | Result |
|---|---|
| `S` empty (first ingest, or after a deep clean) | `Full` |
| `status` is `ahead` | `Touched` and `Removed` from `files` |
| `status` is `behind` or `identical` | stop: a push at or past `after` was already ingested |
| `status` is `diverged` (force push) | `Full` |
| `files` has 300 entries (may be truncated) | `Full` |
| the request returns 5xx (diff too large) | `Full` |
| any other error | fail: nothing is written |

```go
// Fetches a file's contents at a commit; ok is false when the file does not exist.
func (g *Github) FetchFile(ctx context.Context, fullName, path, sha string) ([]byte, error)

// Compares two commits with the GitHub compare API.
func (g *Github) Compare(ctx context.Context, fullName, base, head string) (GithubComparison, error)

// Converts a comparison into touched and removed paths, or marks it Full when it cannot be trusted.
func changedFiles(s string, c GithubComparison, err error) (ChangedFiles, error)
```

#### Validation

1. If `docs.toml` returned 404:
	- If `S` is not empty, the file existed at the last ingest, so run the [[0001-github-doc-parsing-and-caching#Deep clean|deep clean]] and stop.
	- Otherwise the repo was never eligible, stop.

2. Parse `docs.toml` into `DocsConfig`. A TOML syntax error logs a warning and stops.
   Node level errors mark the node invalid with a warning; phase one decides whether it is frozen or skipped.
3. Check that the project file exists. A request is only needed when the answer is not already known:

| Situation                                                | Check                                                                                                                       |
| -------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------- |
| `ChangedFiles.Full`                                      | `FetchFile` the project file                                                                                                |
| `ProjectPath` differs from the cached `Repo.ProjectPath` | `FetchFile` the project file                                                                                                |
| `ProjectPath` is in `ChangedFiles.Removed`               | missing, no request                                                                                                         |
| otherwise                                                | exists, no request: it existed at the last ingest and has not been removed a missing project file logs a warning and stops. |

Stopping at any point in this step writes nothing and leaves `Repo.LastIngestedSHA` unchanged, which freezes the repo's
cached docs and commits until a later push passes validation and catches up from `S`.
Once validation passes, phase one and phase two start concurrently.

```go
// Parses docs.toml, applying defaults and recording nodes with node level errors, each with a logged warning.
func ParseDocsConfig(data []byte, repoName string, logger *slog.Logger) (DocsConfig, error)

// Reports whether the project file exists, making a request only when the comparison cannot answer.
func (r *Repos) projectFileExists(ctx context.Context, cached *Repo, cfg DocsConfig, changes ChangedFiles, sha string) (bool, error)
```

#### Phase one: docs

1. Collect the referenced paths of the cached tree (`before`) and of the parsed tree's nodes without a parse time
   error (`now`): every non empty `DocSection.Path` plus `ProjectPath`.
2. Classify:
	- **new**: in `now`, not in `before`;
	- **outdated**: in both, and in `ChangedFiles.Touched` (every path in both when `ChangedFiles.Full`), plus every path under a node 
	  that was `Frozen` at the last ingest and has no parse time error now.

3. Fetch and render every **new** and **outdated** path concurrently at `event.After`.
   Children are fetched alongside their parents rather than after them; fetching a child of a node that turns out to be
   invalid wastes a request but keeps the fetch to one round trip.
4. A path is missing if its fetch returned 404, or if it is **unchanged** and in `ChangedFiles.Removed`.
   A node is invalid if it has a parse time error or a missing path.
5. Build the effective tree from the parsed tree: replace every invalid node with its frozen cached subtree, or skip it
   when it has no cached counterpart. Discard any HTML fetched for paths inside frozen or skipped subtrees.
6. **old** is every path in `before` that is not in the effective tree, which covers removed tables and their
   descendants as well as files a node no longer points to.
7. Build the new `Repo`: GitHub metadata from the event, settings from `DocsConfig`, the effective `DocSections`,
   `LastCommit` from `HeadCommit`, and `LastIngestedSHA` set to `event.After`.
8. Run the [commit script](#commit-script) with expected SHA `S`.
	* It returns 1: done.
	* It returns 0: another push committed first. Restart from the initial step with the new `S`, up to 3 times;
     the restarted comparison either reports `behind` or `identical` and stops, or covers only the remaining gap.
     Exhausting the retries is a failure.

9. Any error (a non 404 fetch failure, a rendering error, a Redis error, a timeout) is a failure: nothing is written and
   the next push retries the work from `S`.

```go
// Collects every referenced file path in a section tree plus the project file.
func referencedPaths(sections []DocSection, projectPath string) map[string]struct{}

// Classifies referenced paths into new and outdated, including every path under a node being unfrozen.
func classify(before, now map[string]struct{}, changes ChangedFiles, unfrozen map[string]struct{}) PathSets

// Collects every path under a node that was frozen at the last ingest and has no parse time error now.
func unfrozenPaths(parsed, cached []DocSection, invalid map[string]struct{}) map[string]struct{}

// Fetches and renders paths concurrently; returns HTML by path and the set of paths that do not exist.
func (r *Repos) renderAll(ctx context.Context, fullName, sha string, paths []string) (html map[string]string, missing map[string]struct{}, err error)

// Returns the key paths of every node with a parse time error or a missing path.
func invalidNodes(parsed []DocSection, parseInvalid, missing map[string]struct{}) map[string]struct{}

// Builds the tree to cache: each invalid node is replaced by its cached subtree marked Frozen, or skipped if never ingested.
func freezeInvalid(parsed, cached []DocSection, invalid map[string]struct{}) []DocSection

// Renders Markdown into an HTML string.
func renderMarkdown(src []byte) (string, error)
```

#### Phase two: commits

1. Take the comparison's `commits`, or the push event's `Commits` when `ChangedFiles.Full` because of an empty `S` or
   `diverged`.
2. Parse each into a `ConventionalCommit`; drop any commit whose message does not strictly follow the Conventional
   Commits specification. `distinct` is ignored: every commit in range is considered.
3. `ZADD commits` every parsed commit, then `ZREMRANGEBYSCORE commits -inf {now - 90 days}`, then
   `PUBLISH repos:updates commits`.

Phase two does not wait for phase one. If phase one later fails, `S` has not moved, so the next push compares from `S`
again and re-adds the same commits, which leaves the sorted set unchanged.

```go
// Parses a commit into a ConventionalCommit; ok is false when the message does not strictly conform.
func ParseConventionalCommit(repo GithubRepo, sha, message string, committed time.Time) (c ConventionalCommit, ok bool)

// Adds commits to the commit log and prunes entries older than 90 days.
func (r *Repos) logCommits(ctx context.Context, commits []ConventionalCommit) error
```

### Lua scripts

Both scripts compare `last_ingested_sha` inside the stored `Repo` JSON (decoded with `cjson`) against the expected SHA,
treating a missing `repos` field as an empty SHA.
Redis runs a script without interleaving any other command, so the check and the writes are atomic across instances.
`WATCH` on `repos` would also work, but it would abort on any repo's update, not just this one, and costs more round trips.

#### Commit script

* `KEYS`: `repos`, `{Repo.ID}:docs`
* `ARGV`: `Repo.ID`, expected SHA, new `Repo` JSON, the number of **old** paths, the **old** paths, then
  alternating path and HTML pairs for **new** and **outdated**
* If the stored SHA differs from the expected SHA, return 0 without writing.
* Otherwise `HDEL` the old paths, `HSET` the path and HTML pairs, `HSET repos {Repo.ID}` the new JSON,
  `PUBLISH repos:updates repos:{Repo.ID}`, and return 1.

#### Deep clean

* `KEYS`: `repos`, `{Repo.ID}:docs`, `commits`
* `ARGV`: `Repo.ID`, expected SHA
* If the stored SHA differs from the expected SHA, return 0 without writing, so a stale push cannot wipe out a newer
  ingest.
* Otherwise `DEL {Repo.ID}:docs`, `HDEL repos {Repo.ID}`, `ZREM` every member of `commits` whose `repo_id` matches,
  `PUBLISH repos:updates repos:{Repo.ID}`, `PUBLISH repos:updates commits`, and return 1.

Scanning `commits` is linear in its size, which the 90 day prune keeps small.

```go
// Runs the commit script; ok is false when Repo.LastIngestedSHA no longer equals expected.
func (r *Repos) commitDocs(ctx context.Context, repo Repo, expected string, html map[string]string, old []string) (ok bool, err error)

// Runs the deep clean script; ok is false when Repo.LastIngestedSHA no longer equals expected.
func (r *Repos) deepClean(ctx context.Context, repoID int64, expected string) (ok bool, err error)
```

`Client` grows to cover `EvalSha`, `ZAdd`, `ZRemRangeByScore`, and `Publish`.

### Local cache and serving pages

`syncLocalCache` already reloads a repo on `repos:{Repo.ID}`.
It changes in two ways:

* when `HGET repos {Repo.ID}` finds nothing (a deep clean), the repo is removed from the cache instead of logging a warning;
* it maintains a second map from `Repo.Name` to `Repo.ID`, updated on every reload, so a rename takes effect as soon as the
  next successful push rewrites the `Repo` JSON. Old names stop resolving; cached HTML is untouched because it is keyed by ID.

Pages are served at `/{repo name}/{table.segment}/{table.table.segment}/{table.table.table.segment}`:

1. Resolve the repo name to a `Repo` through the name map. An unknown name is a 404.
2. With no further segments, serve the project page: `ProjectPath`.
3. Otherwise walk `Repo.DocSections`, matching one segment per level.
   If the walk does not end on a node with a non empty `Path` (no such segment, a heading only chapter, or too many
   segments), redirect to `/{repo name}`.
4. `HGET {Repo.ID}:docs {path}` and serve the HTML.

```go
// Resolves a page URL to the repo ID and file path whose HTML should be served; ok is false when no page matches.
func (r *Repos) Page(repoName string, segments []string) (repoID int64, path string, ok bool)

// Fetches the cached HTML for a repo's file.
func (r *Repos) HTML(ctx context.Context, repoID int64, path string) (string, error)
```

Repo renames arrive as a separate `repository` webhook event, which this RFC does not handle, so until the next push the
old name keeps working and the new one is a 404.

### The Guide-level examples, revisited

| Example | What happens |
|---|---|
| Edit `docs/install/linux.md` | The comparison touches it, so it is **outdated**; only it is fetched |
| Change a `title` | No referenced path is new, old, or touched; only the `Repo` JSON is rewritten |
| Add `[guides.caching]` | `docs/guides/caching.md` is **new** and is fetched |
| Delete `[guides.database]` | Its key path is gone, so it and its child are removed; both paths are **old** and deleted |
| `path = "docs/install/mac.md"` | The **new** path returns 404; `install.macos` is cached, so its cached subtree is frozen and `docs/install/macos.md` keeps its HTML |
| Fix the path back to `macos.md` | `install.macos` was `Frozen`, so every path in its subtree is **outdated** and refetched |
| Add `[guides.caching]` without its file | The **new** path returns 404 and `guides.caching` was never cached, so it is skipped |
| `segment = "linux"` on `[install.macos]` | Sibling collision at parse time; both nodes are cached, so both are frozen |
| `[guides.database.pooling.tuning]` | Depth 4, ignored at parse time |
| `[project] path = "OVERVIEW.md"` missing | The path differs from the cached one, so it is fetched, returns 404, and validation stops the push |
| `docs.toml` syntax error | Validation stops the push; `S` does not move, so the fixing push compares from `S` and catches up |
| Delete `docs.toml` | 404 with a non empty `S`: deep clean |
| Restore `docs.toml` | The repo is no longer cached, so `S` is empty and the push is a full ingest |

## Prior art
[prior-art]: #prior-art

The direct predecessor is this site's own first attempt, written in Rust (Leptos, axum, comrak) and removed in the
Go rewrite; it can be read at commit `ef118d9`, mainly `src/api/repo_cache.rs`, `src/api/webhooks.rs`,
and `docs/specs/003-dyndocs/design.md`.
It solved the same problem with a different set of trade offs, and most of this RFC is a response to what it got wrong.

### How the legacy system worked

* **Config**: each repo needed `docs/overview.md`, whose JSON front matter registered the sections and whose Markdown
  body became the project home page.
* **Structure**: a section was a directory directly under `docs/` and its pages were the `.md` files inside it,
  so the tree was fixed at two levels and file paths were derived from directory and file names.
* **URLs**: `/docs/{repo name}/{section}/{file}`, slugified from the directory and file names.
* **Redis**: one global `repos:docs` hash keyed by `{id}:docs:{section}:{file}.md`, holding JSON with the HTML and
  extracted h1 and h2 headings; `repos:states` for metadata; a per repo `{id}:commits` sorted set plus a global
  `commits` sorted set, each capped at 50 entries by rank.
* **Change detection**: the push payload's commits, replayed newest to oldest so that a later `removed` shadows an
  earlier `modified`, plus the set difference between the registered paths in the new and cached front matter.
  A cold cache ingested every registered path.
* **Writes**: the new front matter was written and published first, then every changed file was fetched from
  `raw.githubusercontent.com`, rendered, and written with its own `HSET`.
* **Webhook**: verified `X-Hub-Signature-256` with HMAC, ignored non default branch pushes, and ran doc reconciliation
  in a background task.

### What carries over

* The default branch filter, background processing, and reading every file at a single commit.
* Rendered HTML cached in Redis keyed by repo ID, with pub/sub to keep every instance's in memory cache fresh.
  The legacy design doc's sizing still holds: a 250 line Markdown file renders to roughly 30KB of HTML, so even
  5 projects with 100 pages each is about 15MB.
* Diffing the cached tree against the new one, so a config change alone triggers no Markdown fetches.

### What changes, and why

| Legacy behavior                                              | Problem                                                                                                               | This RFC                                                                                |
| ------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------- |
| Config in front matter of a Markdown file                    | Config and content mixed in one file; JSON front matter was awkward enough that switching to YAML was already planned | A dedicated `docs.toml` at the repo root; the project page is an ordinary Markdown file |
| Fixed `docs/{section}/{file}.md` layout                      | Two levels only; URLs tied to file names, so renaming a page meant moving a file                                      | Explicit `path` per node, three levels, and `segment` decoupled from the file           |
| Change detection from the payload's commits                  | A missed delivery, a failed fetch, or a push over 20 commits left pages stale with no recovery                        | Compare `Repo.LastIngestedSHA...after`, so the next push catches up                     |
| Front matter written and published before pages were fetched | Routes could point at HTML that was never cached when a fetch failed                                                  | Nothing is written until every fetch succeeds                                           |
| Each page written with its own `HSET`, failures only logged  | A partial failure left a mix of old and new pages                                                                     | One Lua script writes everything or nothing                                             |
| No coordination between pushes or instances                  | Overlapping pushes could overwrite newer pages with older ones                                                        | The commit script only writes if `Repo.LastIngestedSHA` is unchanged                    |
| Deleted pages never removed (marked "PLANNED" in the code)   | Stale HTML accumulated in Redis                                                                                       | **old** paths are deleted, and removing `docs.toml` deep cleans the repo                |
| One global docs hash                                         | Removing a repo meant scanning keys                                                                                   | One `{Repo.ID}:docs` hash per repo, removed with a single `DEL`                         |
| Commit log capped at 50 entries by rank                      | The log's time span depended on how busy the repos were                                                               | A 90 day window pruned by score                                                         |
| Repo resolved by name with a linear scan                     | Fine at a handful of repos                                                                                            | A name to ID map maintained by the local cache                                          |

### What this RFC drops

* **Heading extraction**: the legacy renderer extracted each page's h1 and h2 headings into a nav tree and cached
  them alongside the HTML. This RFC caches only the HTML string.
* **Per repo commit logs**: only the global `commits` sorted set remains.

## Unresolved questions
[unresolved-questions]: #unresolved-questions

To resolve before implementation:

* **Webhook signature verification**: the legacy system verified `X-Hub-Signature-256` with HMAC SHA256 and a
  `WEBHOOK_SECRET`; decide whether to port it as is, and whether verification happens before the 202.
* **GitHub API authentication and rate limits**: which token the server uses (a personal access token or a GitHub App),
  how it is configured, and what happens when the authenticated limit of 5,000 requests an hour is reached
  (today a rate limit is an ordinary failure that the next push retries).
* **Markdown renderer**: whether `renderMarkdown` uses `pkg/flavor` or an existing library, and which GitHub Flavored
  Markdown extensions it supports (the legacy system used comrak with tables, task lists, footnotes, autolinks,
  strikethrough, and line numbered code fences).
* **Commit timestamp source**: confirm that the push payload's `commits[].timestamp` equals the comparison's
  `commit.committer.date` for the same commit; if it does not, the fallback path must use the same source as the
  comparison or a commit can be logged twice.
* **Repo rename**: if a Github repo is renamed how will the project URL and cached values be adjusted.

To resolve during implementation:

* **Retry cap and timeout**: 3 retries after a lost commit check and a 60 second background timeout are starting
  values; adjust them once real ingestion times are known.
* **Blacklist contents**: the exact set of repo names reserved for site routes.

Out of scope:

* Handling the `repository` webhook event, so renames, deletions, and transfers take effect without a push.
* Pushes to branches other than the default branch.

## Future possibilities
[future-possibilities]: #future-possibilities

* **Rename events**: subscribe to `repository` events with `action: "renamed"` and rewrite only the `Repo` JSON,
  closing the gap where a renamed repo's new URL is a 404 until its next push.
* **Heading extraction**: restore the legacy system's h1 and h2 extraction so each page can render an on page
  table of contents; the headings would be cached alongside the HTML.
* **Frozen indicator**: a display only flag set when validation fails and cleared on the next successful ingest,
  so the project page can show that its docs are out of date and why.
* **Coalescing work queue**: the webhook handler records the latest `after` per repo in Redis and a worker on either
  instance processes it; failed work is retried on a backoff instead of waiting for the next push.
* **SHA keyed commit log**: storing the commit SHA as the `commits` sorted set member, with the JSON kept in a separate
  hash, would make deduplication independent of byte identical JSON.
* **Search**: the cached HTML for every page is already in Redis, which makes a site wide docs search a natural next step.
