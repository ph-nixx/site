package app

import (
	"errors"
	"html/template"
	"io"
	"net/http"

	"github.com/ph-nixx/site/cmd/web/repos"
)

// One sidebar entry: a chapter or section, linking to Href unless it is a heading only chapter.
type sidebarEntry struct {
	Title    string
	Href     string // "/{repo name}/{route}", empty for a heading only chapter
	Route    string // stacked segments, matched against the current page by the htmx:afterSwap handler
	Children []sidebarEntry
}

// The data the doc template renders on a full page load.
type docPage struct {
	Repo    *repos.Repo
	HTML    template.HTML // rendered by goldmark with raw HTML omitted, so it is emitted unescaped
	Sidebar []sidebarEntry
	Nav     navData
}

// Serves a docs page at /{repo name}/{segments...}: the doc's HTML alone for an HX-Request, otherwise the full page
// with the sidebar; 404 for an unknown repo and a redirect to /{repo name} for an unknown route.
func (a *App) doc(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	// partial and full responses share a URL, so a cache must not serve one for the other
	w.Header().Set("Vary", "HX-Request")
	// htmx restoring history it has no snapshot for sends HX-Request too, but expects the full page
	partial := r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-History-Restore-Request") != "true"

	html, err := a.repos.Doc(r.Context(), r.URL.Path)
	switch {
	case errors.Is(err, repos.ErrUnknownRepo):
		a.renderErr(w, r, http.StatusNotFound, nil)
		return
	case errors.Is(err, repos.ErrUnknownPath) && r.PathValue("route") == "":
		a.renderErr(w, r, http.StatusNotFound, nil) // the project page itself is missing: redirecting to it would loop
		return
	case errors.Is(err, repos.ErrUnknownPath) && partial:
		// htmx would swap a followed redirect into #doc; HX-Redirect makes it a full page load with a fresh sidebar
		w.Header().Set("HX-Redirect", "/"+name)
		return
	case errors.Is(err, repos.ErrUnknownPath):
		http.Redirect(w, r, "/"+name, http.StatusFound)
		return
	case err != nil:
		a.renderErr(w, r, http.StatusInternalServerError, err)
		return
	}

	if partial {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, html)
		return
	}
	repo, ok := a.repos.GetByName(name)
	if !ok {
		a.renderErr(w, r, http.StatusNotFound, nil) // removed by a deep clean between the two lookups
		return
	}

	var entries func(prefix string, sections []repos.DocSection) []sidebarEntry
	entries = func(prefix string, sections []repos.DocSection) []sidebarEntry {
		var built []sidebarEntry
		for _, s := range sections {
			route := prefix + s.Segment
			e := sidebarEntry{Title: s.Title, Route: route, Children: entries(route+"/", s.SubSections)}
			if s.Path != "" {
				e.Href = "/" + name + "/" + route
			}
			built = append(built, e)
		}
		return built
	}
	all, _ := a.repos.Get()
	a.render(w, r, "doc", docPage{Repo: repo, HTML: template.HTML(html), Sidebar: entries("", repo.DocSections), Nav: navData{Repos: all, Current: name}})
}
