package app

import (
	"net/http"

	"github.com/ph-nixx/site/cmd/web/repos"
)

// How many commits the build log on the home page shows, newest first.
const LOG_ROWS = 6

// One row of the build log: the commit's day, its repo, and its header, linking to the commit on GitHub.
type logEntry struct {
	Date    string // in UTC, e.g. "Oct 25"
	Project string // the repo's current name, resolved through the local cache
	Message string // the Conventional Commits header, e.g. "feat(docs)!: add the pooling guide"
	Href    string
}

// The data the home template renders.
type homePage struct {
	Repos []*repos.Repo
	Log   []logEntry // the newest LOG_ROWS commits
	Nav   navData
}

func (a *App) home(w http.ResponseWriter, r *http.Request) {
	all, _ := a.repos.Get()
	page := homePage{Repos: all, Nav: navData{Repos: all}}
	for _, c := range a.repos.Commits() {
		if len(page.Log) == LOG_ROWS {
			break
		}
		repo, ok := a.repos.Get(c.RepoID)
		if !ok {
			continue // a deep clean of its repo is still reaching this instance
		}
		header := c.Type
		if c.Scope != "" {
			header += "(" + c.Scope + ")"
		}
		if c.Breaking {
			header += "!"
		}
		page.Log = append(page.Log, logEntry{
			Date:    c.Timestamp.UTC().Format("Jan 2"),
			Project: repo[0].Name,
			Message: header + ": " + c.Description,
			Href:    "https://github.com/" + repo[0].FullName + "/commit/" + c.ID,
		})
	}
	a.render(w, r, "home", page)
}

// colSpans[k] is the layout for k+1 repos; the last row also serves any larger count.
// Class names stay literal so Tailwind's scanner can find them.
var colSpans = [][]string{
	{"md:col-span-12"},
	{"md:col-span-12", "md:col-span-12"},
	{"md:col-span-6", "md:col-span-6", "md:col-span-12"},
	{"md:col-span-12", "md:col-span-4", "md:col-span-4", "md:col-span-4"},
	{"md:col-span-6", "md:col-span-6", "md:col-span-4", "md:col-span-4", "md:col-span-4"},
}

// colSpan returns the grid-span class for card i of n, or "" if that card isn't shown.
func colSpan(n, i int) string {
	if n < 1 || i < 0 {
		return ""
	}
	row := colSpans[min(n, len(colSpans))-1]
	if i >= len(row) {
		return ""
	}
	return row[i]
}
