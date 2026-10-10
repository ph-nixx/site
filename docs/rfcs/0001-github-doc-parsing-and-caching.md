---
feature_name: github_doc_parsing_and_caching
start_date: 2026-10-09
status: open
---

## Summary
[summary]: #summary

When a GitHub push event arrives for an eligible repo, the server brings its Redis cache in line with the repo's
current documentation and commit history.
The work is split into an initial sequential step followed by two independent phases that can run concurrently.

**Initial step.** Fetch `docs/docs.toml` from the repo's default branch via the GitHub API.
This file is the single source of truth for the repo's docs structure; it is parsed into `[]DocSection`,
a recursive type (`DocSection.SubSections []DocSection`) at most three levels deep (chapter → section → subsection).
A repo is eligible only if `docs/docs.toml` exists (it may be empty) and its project file
(`[project].path`, defaulting to `README.md`) exists.

**Redis layout.**

* `repos`: a hash mapping each `Repo.ID` to its `Repo` JSON string, including `Repo.DocSections`.
  Keeping metadata in one hash makes fetching every repo for the landing page a single `HGETALL`.
* `{Repo.ID}:docs`: a hash mapping each ingested Markdown file's path, relative to the repo root
  (e.g. `README.md`, `docs/tutorials/database/connect.md`), to its rendered HTML string.
  One hash per repo gives each repo its own path namespace and makes removing a repo's docs a single `DEL`.
  Field keys match the paths GitHub reports in a commit's `added`, `modified`, and `removed` lists, so no translation is needed.
* `commits`: a sorted set of `ConventionalCommit` JSON strings scored by commit timestamp, aggregated across all repos.

**Phase one: docs.** Compare the cached `Repo.DocSections` with the freshly parsed one, together with the push's commits,
to classify every referenced Markdown path:

* **new**: referenced now but not before: fetch, parse to HTML, `HSET`.
* **outdated**: referenced before and now, and listed in a commit's `added` or `modified`: fetch, parse to HTML, `HSET`.
* **old**: referenced before but not now: `HDEL`.
* **unchanged**: everything else: no work.

A section whose file does not exist is invalid, and its entire subtree is dropped from `Repo.DocSections` before the diff,
so any previously cached paths in that subtree fall into **old**.
A chapter's `path` is optional; a chapter without one is a non-clickable heading and contributes no path.
Fetches for **new** and **outdated** paths run concurrently.
Finally, the `Repo` JSON string is overwritten unconditionally, since one `HSET` is cheaper than diffing the metadata.

**Phase two: commits.** Parse the push's commits into `ConventionalCommit` values, add them to `commits`,
then prune every member whose score is older than the current UTC time minus 90 days.

## Guide-level explanation
[guide-level-explanation]: #guide-level-explanation

Explain the feature as if it was already existed and you were teaching a friend how to use it. That generally means:

* Introducing new named concepts.
* Explaining the feature largely in terms of examples.
* Explaining how your friend should *think* about the feature. It should explain the impact as concretely as possible.

## Reference-level explanation
[reference-level-explanation]: #reference-level-explanation

This is the technical portion of the RFC. Explain the design in sufficient detail that:

* Its interaction with other features is clear.
* It is reasonably clear how the feature would be implemented.
* Corner cases are dissected by example.

The section should return to the examples given in the previous section, and explain more fully how the detailed proposal makes those examples work.

The `docs.toml` file must exist for the project to be eligable, `docs.toml` can be empty and still project can still be eligable because everything will assume its default value.

```toml
[project] # optional, slug only required
name = "Name" # optional, defaults to name of the repo with first character capitialized
path = "x/y.md" # optional, assumed relative to the root of the project and must end with .md, defaults to README.md
svg = '<svg></svg>' # optional

[project-chapter-beta] # optional, slug only required
path = "x/y/z.md" optional
segment = "a" # optional, defaults to table name e.g. "project-chapter-beta"
title = "Project Chapter Beta" # optional, defaults to the space seperated table name with the first character capitialized e.g. "Project chapter beta"

[project-chapter-beta.section-a] # optional, slug only required
path = "x/y/z.md" # required, assumed relative to docs directory at the root of the project, must end with .md
segment = "b" # optional, defaults to sub table name e.g. "section-a"
title = "Section A (updated)" # optional, defaults to space seperated sub table name with first character capitialized
							 # e.g. "Section a"

# nesting can only go upto a depth of 3 then everything else is ignored
[project-chapter-beta.section-a.section-b]
```

## Prior art
[prior-art]: #prior-art

Discuss prior art, both the good and the bad, in relation to this proposal.
An examples of what this can include is: does this feature exist in other projects and what experience have their community had?

This section is intended to encourage you as an author to think about the lessons from other projects, provide readers of your RFC with a fuller picture. If there is no prior art, that is fine - your ideas are interesting to us whether they are brand new or if it is an adaptation from other languages. Note that while precedent set by other projects is some motivation, it does not on its own motivate an RFC.

## Unresolved questions
[unresolved-questions]: #unresolved-questions

* What parts of the design do you expect to resolve through the RFC process before this gets merged?
* What parts of the design do you expect to resolve through the implementation of this feature before stabilization?
* What related issues do you consider out of scope for this RFC that could be addressed in the future independently of the solution that comes out of this RFC?

## Future possibilities
[future-possibilities]: #future-possibilities

Think about what the natural extension and evolution of your proposal would be and how it would affect the language and project as a whole in a holistic way. Try to use this section as a tool to more fully consider all possible interactions with the project and language in your proposal. Also consider how this all fits into the roadmap for the project and of the implementers.
This is also a good place to "dump ideas", if they are out of scope for the RFC you are writing but otherwise related. If you have tried and cannot think of any future possibilities, you may simply state that you cannot think of anything. Note that having something written down in the future-possibilities section is not a reason to accept the current or a future RFC; such notes should be in the section on motivation or rationale in this or subsequent RFCs. The section merely provides additional information.
