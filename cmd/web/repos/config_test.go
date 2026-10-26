package repos

import (
	"maps"
	"testing"

	"github.com/google/go-github/v91/github"
)

// [reconcileDocsConfig] warns about every invalid node, saying whether it is frozen at its cached version or skipped,
// and about every table nested too deep, while the rest of the tree reconciles normally.
func Test_reconcile_docs_config_warns_about_invalid_nodes(t *testing.T) {
	docsToml := File{Status: FileFound, Data: []byte(`
[getting-started]
path = "docs/getting-started.md"

[install]

[install.linux]
path = "docs/install/linux.txt"

[install.windows]

[Guides]
path = "docs/guides.md"

[reference.api.v1]
path = "docs/reference/v1.md"

[reference.api.v1.deprecated]
path = "docs/reference/deprecated.md"

[faq]
segment = "help"
path = "docs/faq.md"

[help]
path = "docs/help.md"

[about]
title = 5
path = "docs/about.md"

[blog]
segment = "Blog"
`)}
	cached := &Repo{LastIngestedSHA: "a", DocSections: []DocSection{
		{Key: "getting-started", Segment: "getting-started", Path: "docs/getting-started.md"},
		{Key: "install", Segment: "install", SubSections: []DocSection{
			{Key: "install.linux", Segment: "linux", Path: "docs/install/linux.md"},
		}},
	}}
	event := &github.PushEvent{After: github.Ptr("b"), Repo: &github.PushEventRepository{Name: github.Ptr("one")}}
	comp := &github.CommitsComparison{Status: github.Ptr("ahead"), Files: []*github.CommitFile{
		{Filename: github.Ptr("docs/getting-started.md"), Status: github.Ptr("removed")},
	}}

	cfg, err := reconcileDocsConfig(docsToml, File{Status: FileFound}, event, cached, comp, nil)
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}
	for _, w := range cfg.Warnings {
		got[w.Key] = w.Reason
	}
	want := map[string]string{
		"getting-started": "its file was removed in the comparison; frozen at its cached version",
		"install.linux":   `invalid in docs.toml: its path "docs/install/linux.txt" does not end in .md; frozen at its cached version`,
		"install.windows": "invalid in docs.toml: it is a section without a path; skipped with its subtree, since it was never ingested",
		"Guides":          `invalid in docs.toml: its table name "Guides" is not a slug; skipped with its subtree, since it was never ingested`,
		"reference.api":   "invalid in docs.toml: it is a section without a path; skipped with its subtree, since it was never ingested",
		"faq":             `invalid in docs.toml: its segment "help" is shared with a sibling; skipped with its subtree, since it was never ingested`,
		"help":            `invalid in docs.toml: its segment "help" is shared with a sibling; skipped with its subtree, since it was never ingested`,
		"about":           "invalid in docs.toml: its title is not a string; skipped with its subtree, since it was never ingested",
		"blog":            `invalid in docs.toml: its segment "Blog" is not a slug; skipped with its subtree, since it was never ingested`,

		"reference.api.v1.deprecated": "nested deeper than 3 levels; ignored with its subtree",
	}
	if !maps.Equal(got, want) {
		t.Errorf("expected warnings %v, got %v", want, got)
	}
}
