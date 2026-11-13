package app

import (
	"math"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/ph-nixx/site/cmd/web/repos"
)

// How many commits the build log on the home page shows, newest first.
const LOG_ROWS = 6

// The window the terminal's stats cover. Must stay within repos.COMMIT_RETENTION.
const TERM_RANGE = 30 * 24 * time.Hour

// Typing pace of the terminal's command line, matching the term-type rules in input.css.
const (
	TERM_TYPE_CHAR_MS  = 50  // per character
	TERM_TYPE_PAUSE_MS = 200 // between the project name and its flags
)

// One row of the build log: the commit's day, its repo, and its header, linking to the commit on GitHub.
type logEntry struct {
	Date    string // in UTC, e.g. "Oct 25"
	Project string // the repo's current name, resolved through the local cache
	Message string // the Conventional Commits header, e.g. "feat(docs)!: add the pooling guide"
	Href    string
}

// The hero terminal's readout of the most active project over TERM_RANGE.
type termStats struct {
	Project  string // the repo's Title, typed as the command
	Name     string // the repo's Name: the typed Title links to its route, /{Name}
	Range    string // formatDuration of the interval, e.g. "30d": typed as the --range flag
	Commits  int    // its commits within the range
	Share    int    // Commits as a whole percent of every repo's commits within the range
	Features int    // its "feat" commits within the range
	Last     string // formatDuration from its newest commit to before, e.g. "3h": rendered as "last commit 3h ago"

	// Typing choreography, rendered into inline CSS custom properties.
	NameChars int // characters in Project: the name span's --term-n
	FlagChars int // characters in " --range " + Range: the flags span's --term-n
	FlagsAt   int // ms into typing the flags start: NameChars*TERM_TYPE_CHAR_MS + TERM_TYPE_PAUSE_MS
	TypedMS   int // ms into typing the command finishes: FlagsAt + FlagChars*TERM_TYPE_CHAR_MS
}

// The data the home template renders.
type homePage struct {
	Repos []*repos.Repo
	Log   []logEntry // the newest LOG_ROWS commits
	Term  *termStats // nil when no repo has a commit within TERM_RANGE
	Nav   navData
}

func (a *App) home(w http.ResponseWriter, r *http.Request) {
	all, _ := a.repos.Get()
	now := time.Now()
	page := homePage{Repos: all, Nav: navData{Repos: all}, Term: mostActive(a.repos, now.Add(-TERM_RANGE), now)}
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
			Project: repo[0].Title,
			Message: header + ": " + c.Description,
			Href:    "https://github.com/" + repo[0].FullName + "/commit/" + c.ID,
		})
	}
	a.render(w, r, "home", page)
}

// mostActive picks the repo with the most commits timestamped in (after, before] (ties go to the newest commit) and returns its stats, or nil if none.
func mostActive(rs *repos.Repos, after, before time.Time) *termStats {
	type tally struct {
		id                int64
		title, name       string
		commits, features int
		newest            time.Time
	}
	tallies := map[int64]*tally{}
	total := 0
	for _, c := range rs.Commits() {
		if !c.Timestamp.After(after) {
			break // newest first, so every later commit is outside the interval too
		}
		if c.Timestamp.After(before) {
			continue
		}
		t, ok := tallies[c.RepoID]
		if !ok {
			repo, ok := rs.Get(c.RepoID)
			if !ok {
				continue // a deep clean of its repo is still reaching this instance
			}
			t = &tally{id: c.RepoID, title: repo[0].Title, name: repo[0].Name, newest: c.Timestamp}
			tallies[c.RepoID] = t
		}
		t.commits++
		if c.Type == "feat" {
			t.features++
		}
		total++
	}

	var best *tally
	for _, t := range tallies {
		switch {
		case best == nil, t.commits > best.commits:
			best = t
		case t.commits < best.commits:
		case t.newest.After(best.newest), t.newest.Equal(best.newest) && t.id < best.id:
			best = t
		}
	}
	if best == nil {
		return nil
	}

	span := formatDuration(after, before)
	nameChars := utf8.RuneCountInString(best.title)
	flagChars := utf8.RuneCountInString(" --range " + span)
	flagsAt := nameChars*TERM_TYPE_CHAR_MS + TERM_TYPE_PAUSE_MS
	return &termStats{
		Project:   best.title,
		Name:      best.name,
		Range:     span,
		Commits:   best.commits,
		Share:     int(math.Round(float64(best.commits) * 100 / float64(total))),
		Features:  best.features,
		Last:      formatDuration(best.newest, before),
		NameChars: nameChars,
		FlagChars: flagChars,
		FlagsAt:   flagsAt,
		TypedMS:   flagsAt + flagChars*TERM_TYPE_CHAR_MS,
	}
}

// formatDuration formats before-after as whole hours, never below 1, while that rounds under 24, e.g. "3h", else as whole days, e.g. "30d".
func formatDuration(after, before time.Time) string {
	hours := before.Sub(after).Hours()
	if h := int(math.Round(hours)); h < 24 {
		return strconv.Itoa(max(h, 1)) + "h"
	}
	return strconv.Itoa(int(math.Round(hours/24))) + "d"
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
