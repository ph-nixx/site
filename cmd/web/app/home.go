package app

import "net/http"

func (a *App) home(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "home", a.repos.Get())
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
